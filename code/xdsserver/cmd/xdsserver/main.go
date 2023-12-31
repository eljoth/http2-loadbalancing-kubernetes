package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync/atomic"
	"time"

	cluster "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	ep "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	listener "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	route "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	router "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/router/v3"
	hcm "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	discoveryv3 "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	"github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	xds "github.com/envoyproxy/go-control-plane/pkg/server/v3"
	"github.com/envoyproxy/go-control-plane/pkg/wellknown"
	"github.com/google/uuid"
	"github.com/kelseyhightower/envconfig"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

var version = int32(0)

type Address struct {
	IP   string
	Port int
}

type Config struct {
	ClusterName   string        `default:"sem-xds"`
	Namespace     string        `default:"xds"`
	ListenerName  string        `default:"sem-xds"`
	LabelSelector string        `default:"app=server"`
	Port          int           `default:"8090"`
	Interval      time.Duration `default:"10s"`
	ServerPort    int           `default:"8082"`
}

type Server struct {
	Cache     cache.SnapshotCache
	Endpoints []*ep.LbEndpoint
	uuid      uuid.UUID
}

func main() {
	var config Config
	if err := envconfig.Process("", &config); err != nil {
		os.Exit(1)
	}

	log.Println(config)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	c := cache.NewSnapshotCache(false, cache.IDHash{}, nil)
	s := Server{
		Cache: c,
		uuid:  uuid.New(),
	}

	srv := xds.NewServer(ctx, s.Cache, nil)

	ch := make(chan struct{})
	eg, ctx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		return s.serve(ctx, ch, srv, config)
	})

	<-ch

	eg.Go(func() error {
		return s.updateEndpoints(ctx, config)
	})

	if err := eg.Wait(); err != nil {
		os.Exit(1)
	}

}

// getAddresses returns the addresses of the pods matching the labelSelector.
func (s Server) getAddresses(ctx context.Context, labelSelector, namespace string, port int) ([]Address, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("error creating in-cluster config: %s", err.Error())
	}

	clientSet, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("error creating clientset: %s", err.Error())
	}

	pods, err := clientSet.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})

	if err != nil {
		return nil, fmt.Errorf("error getting pods: %s", err.Error())
	}

	var podIPs []Address
	for _, pod := range pods.Items {
		log.Println(pod.Status.PodIP)
		podIPs = append(podIPs,
			Address{
				IP:   pod.Status.PodIP,
				Port: port,
			})
	}

	return podIPs, nil
}

func (s Server) updateEndpoints(ctx context.Context, config Config) error {
	var addresses []Address
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
			newAddresses, err := s.getAddresses(ctx, config.LabelSelector, config.Namespace, config.ServerPort)
			if err != nil {
				log.Println(err)
				continue
			}
			if len(newAddresses) != len(addresses) {
				addresses = newAddresses
				for _, address := range addresses {
					if err := s.updateSnapshotCache(ctx, address, config); err != nil {
						log.Println(err)
						continue
					}
				}
			}
			<-time.After(config.Interval)
		}
	}
}
func (s Server) serve(ctx context.Context, ch chan struct{}, server xds.Server, config Config) error {
	var grpcOptions []grpc.ServerOption
	grpcServer := grpc.NewServer(grpcOptions...)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", config.Port))
	if err != nil {
		close(ch)
		return err
	}

	discoveryv3.RegisterAggregatedDiscoveryServiceServer(grpcServer, server)

	errChan := make(chan error)
	go func() {
		if err = grpcServer.Serve(lis); err != nil {
			errChan <- err
		}
	}()

	close(ch)

	select {
	case err := <-errChan:
		return err
	case <-ctx.Done():
		grpcServer.GracefulStop()
	}
	return nil
}

func (s Server) updateSnapshotCache(ctx context.Context, address Address, config Config) error {
	hst := &corev3.Address{Address: &corev3.Address_SocketAddress{
		SocketAddress: &corev3.SocketAddress{
			Address:  address.IP,
			Protocol: corev3.SocketAddress_TCP,
			PortSpecifier: &corev3.SocketAddress_PortValue{
				PortValue: uint32(address.Port),
			},
		},
	}}

	epp := &ep.LbEndpoint{
		HostIdentifier: &ep.LbEndpoint_Endpoint{
			Endpoint: &ep.Endpoint{
				Address: hst,
			}},
		HealthStatus: corev3.HealthStatus_HEALTHY,
	}

	s.Endpoints = append(s.Endpoints, epp)

	eds := []types.Resource{
		&endpointv3.ClusterLoadAssignment{
			ClusterName: config.ClusterName,
			Endpoints: []*ep.LocalityLbEndpoints{{
				Locality: &corev3.Locality{
					Region: "us-central1",
					Zone:   "us-central1-a",
				},
				LbEndpoints: s.Endpoints,
			}},
		},
	}

	cls := []types.Resource{
		&cluster.Cluster{
			Name:                 config.ClusterName,
			LbPolicy:             cluster.Cluster_ROUND_ROBIN,
			ClusterDiscoveryType: &cluster.Cluster_Type{Type: cluster.Cluster_EDS},
			EdsClusterConfig: &cluster.Cluster_EdsClusterConfig{
				EdsConfig: &corev3.ConfigSource{
					ConfigSourceSpecifier: &corev3.ConfigSource_Ads{},
				},
			},
		},
	}

	rds := []types.Resource{
		&route.RouteConfiguration{
			ValidateClusters: &wrapperspb.BoolValue{Value: true},
			VirtualHosts: []*route.VirtualHost{{
				Domains: []string{config.ListenerName}, //******************* >> must match what is specified at xds:/// //
				Routes: []*route.Route{{
					Match: &route.RouteMatch{
						PathSpecifier: &route.RouteMatch_Prefix{
							Prefix: "",
						},
					},
					Action: &route.Route_Route{
						Route: &route.RouteAction{
							ClusterSpecifier: &route.RouteAction_Cluster{
								Cluster: config.ClusterName,
							},
						},
					},
				},
				},
			}},
		},
	}

	hcRds := &hcm.HttpConnectionManager_Rds{
		Rds: &hcm.Rds{
			ConfigSource: &corev3.ConfigSource{
				ResourceApiVersion: corev3.ApiVersion_V3,
				ConfigSourceSpecifier: &corev3.ConfigSource_Ads{
					Ads: &corev3.AggregatedConfigSource{},
				},
			},
		},
	}

	hff := &router.Router{}
	tctx, err := anypb.New(hff)
	if err != nil {
		return err
	}

	manager := &hcm.HttpConnectionManager{
		CodecType:      hcm.HttpConnectionManager_AUTO,
		RouteSpecifier: hcRds,
		HttpFilters: []*hcm.HttpFilter{{
			Name: wellknown.Router,
			ConfigType: &hcm.HttpFilter_TypedConfig{
				TypedConfig: tctx,
			},
		}},
	}

	pbst, err := anypb.New(manager)
	if err != nil {
		panic(err)
	}

	l := []types.Resource{
		&listener.Listener{
			ApiListener: &listener.ApiListener{
				ApiListener: pbst,
			},
		}}

	atomic.AddInt32(&version, 1)

	resources := map[resource.Type][]types.Resource{
		resource.ClusterType:  cls,
		resource.EndpointType: eds,
		resource.ListenerType: l,
		resource.RouteType:    rds,
	}

	snap, err := cache.NewSnapshot(fmt.Sprint(version), resources)
	if err != nil {
		return err
	}

	err = s.Cache.SetSnapshot(ctx, s.uuid.String(), snap)
	if err != nil {
		return err
	}
	return nil
}
