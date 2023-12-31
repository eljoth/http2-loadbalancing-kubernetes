package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"context"

	pb "github.com/eljoth/masterseminar/code/server/pkg/api/pb/server/v1"
	grpcPrometheus "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	"github.com/kelseyhightower/envconfig"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	_ "go.uber.org/automaxprocs"
	"golang.org/x/sync/errgroup"
	_ "google.golang.org/grpc/xds"
)

type config struct {
	HeadlessAddress    string        `envconfig:"HEADLESS_ADDRESS" default:":8080"`
	ServiceMeshAddress string        `envconfig:"SERVICE_MESH_ADDRESS" default:":8080"`
	RESTAddress        string        `envconfig:"REST_ADDRESS" default:":8081"`
	XDSAddress         string        `envconfig:"GRPC_ADDRESS" default:":8082"`
	DefaultWaitTime    time.Duration `envconfig:"DEFAULT_WAIT_TIME" default:"1s"`
	gRPCXDSBootrap     string        `envconfig:"GRPC_XDS_BOOTSTRAP" default:"bootstrap.json"`
}

const (
	path                  = "/api/v1/sqrt"
	randLimit             = 1000000000000
	restWorkerName        = "REST"
	headlessWorkerName    = "HEADLESS"
	serviceMeshWorkerName = "SERVICE_MESH"
	xDSWorkerName         = "XDS"
)

var (
	histogramBuckets = []float64{0.001, 0.01, 0.1, 0.3, 0.6, 1, 3, 6, 9, 20, 30, 60, 90, 120}
)

type httpResponse struct {
	Result float64 `json:"result"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "an error occurred: %s\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill, syscall.SIGTERM)
	defer cancel()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	rest := flag.Bool("rest", false, "Enable REST Calls")
	headless := flag.Bool("headless", false, "Enable Headless Calls")
	serviceMesh := flag.Bool("service-mesh", false, "Enable Service Mesh Calls")
	xDS := flag.Bool("xds", false, "Enable xDS Calls")
	flag.Parse()

	var cfg config
	if err := envconfig.Process("", &cfg); err != nil {
		return err
	}

	eg, ctx := errgroup.WithContext(ctx)
	if *rest {
		eg.Go(restWorker(ctx, logger, cfg))
	}

	if *headless {
		eg.Go(headlessWorker(ctx, logger, cfg))
	}

	if *serviceMesh {
		eg.Go(serviceMeshWorker(ctx, logger, cfg))
	}

	if *xDS {
		eg.Go(xDSWorker(ctx, logger, cfg))
	}

	eg.Go(exposeMetrics(ctx))

	if err := eg.Wait(); err != nil {
		return err
	}

	return nil
}

func exposeMetrics(ctx context.Context) func() error {
	server := &http.Server{
		Addr:              ":9000",
		Handler:           http.DefaultServeMux,
		ReadHeaderTimeout: time.Second * 5,
	}
	return func() error {
		http.Handle("/metrics", promhttp.Handler())
		go func() {
			_ = server.ListenAndServe()
		}()
		<-ctx.Done()
		if err := server.Close(); err != nil {
			return err
		}
		return nil
	}
}
func restWorker(ctx context.Context, log *slog.Logger, cfg config) func() error {

	histogramOptions := prometheus.HistogramOpts{
		Buckets: histogramBuckets,
		Name:    "grpc_client_handling_seconds", // Name as the rest of the metrics to make dashboard repeatable
		Help:    "Histogram of response latency (seconds) of the http until it is finished by the application.",
	}
	histogram := prometheus.NewHistogramVec(
		histogramOptions,
		[]string{"http_service", "http_method"},
	)

	prometheus.MustRegister(histogram)

	return func() error {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.DisableKeepAlives = true
		client := http.Client{
			Timeout:   5 * time.Second,
			Transport: transport,
		}
		log.Info("starting worker", slog.String("worker", restWorkerName))

		for {
			select {
			case <-ctx.Done():
				log.Info("stopping worker", slog.String("worker", restWorkerName))
				return nil
			default:
				start := time.Now()
				rn := rand.Intn(randLimit) + 1
				url := fmt.Sprintf("http://%s%s/%d", cfg.RESTAddress, path, rn)
				req, err := http.NewRequest(http.MethodGet, url, nil)
				if err != nil {
					log.Error("creating request", err, slog.String("worker", restWorkerName))
					continue
				}

				resp, err := client.Do(req)
				if err != nil {
					log.Error("calling", err, slog.String("worker", restWorkerName))
					continue
				}

				var res httpResponse
				if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
					log.Error("decoding response", err, slog.String("worker", restWorkerName))
					continue
				}

				log.Info("result", slog.Float64("result", res.Result), slog.String("worker", restWorkerName))

				if err := resp.Body.Close(); err != nil {
					log.Error("closing response body", err, slog.String("worker", restWorkerName))
				}
				histogram.WithLabelValues("SQRT", "GET").Observe(time.Since(start).Seconds())
				<-time.After(cfg.DefaultWaitTime)
			}
		}
	}
}

func headlessWorker(ctx context.Context, log *slog.Logger, cfg config) func() error {
	if !strings.HasPrefix(cfg.HeadlessAddress, "dns:///") {
		cfg.HeadlessAddress = fmt.Sprintf("%s%s", "dns:///", cfg.HeadlessAddress)
	}
	return gRPCRunner(ctx, log, cfg, headlessWorkerName)
}

func serviceMeshWorker(ctx context.Context, log *slog.Logger, cfg config) func() error {
	if !strings.HasPrefix(cfg.ServiceMeshAddress, "dns:///") {
		cfg.ServiceMeshAddress = fmt.Sprintf("%s%s", "dns:///", cfg.ServiceMeshAddress)
	}
	return gRPCRunner(ctx, log, cfg, serviceMeshWorkerName)
}

func xDSWorker(ctx context.Context, log *slog.Logger, cfg config) func() error {
	if !strings.HasPrefix(cfg.XDSAddress, "xds:///") {
		cfg.XDSAddress = fmt.Sprintf("%s%s", "xds:///", cfg.XDSAddress)
	}

	return gRPCRunner(ctx, log, cfg, xDSWorkerName)
}

func gRPCRunner(ctx context.Context, log *slog.Logger, cfg config, name string) func() error {

	var address string
	switch name {
	case xDSWorkerName:
		address = cfg.XDSAddress
	case serviceMeshWorkerName:
		address = cfg.ServiceMeshAddress
	case headlessWorkerName:
		fallthrough
	default:
		address = cfg.HeadlessAddress
	}

	return func() error {

		clientMetrics := grpcPrometheus.NewClientMetrics(
			grpcPrometheus.WithClientHandlingTimeHistogram(
				grpcPrometheus.WithHistogramBuckets(histogramBuckets),
			),
		)

		prometheus.MustRegister(clientMetrics)
		opts := []grpc.DialOption{
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithChainUnaryInterceptor(clientMetrics.UnaryClientInterceptor()),
			grpc.WithDefaultServiceConfig(`{"loadBalancingConfig": [ { "round_robin": {} } ]}`),
		}

		conn, err := grpc.Dial(address, opts...)
		if err != nil {
			return err
		}
		defer func() {
			if err := conn.Close(); err != nil {
				log.Error("closing connection", err, slog.String("worker", name))
			}
		}()

		client := pb.NewServerServiceClient(conn)
		log.Info("starting worker", slog.String("worker", name))
		for {
			select {
			case <-ctx.Done():
				log.Info("stopping worker", slog.String("worker", name))
				return nil
			default:
				rn := rand.Intn(randLimit) + 1
				resp, err := client.GetSQRT(ctx, &pb.GetSQRTRequest{Number: int64(rn)})
				if err != nil {
					log.Error("calling", err, slog.String("worker", name))
					continue
				}
				log.Info("result", slog.Float64("result", float64(resp.Result)), slog.String("worker", name))
				<-time.After(cfg.DefaultWaitTime)
			}
		}
	}
}
