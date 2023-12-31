package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	pb "github.com/eljoth/masterseminar/code/server/pkg/api/pb/server/v1"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	grpcPrometheus "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	_ "go.uber.org/automaxprocs"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/xds"
)

const (
	delta       = 1.0e-3
	grpcAddress = ":8080"
	restAddress = ":8081"
	xDSAddress  = ":8082"
)

type gRPCServer struct {
	log *slog.Logger
	pb.UnimplementedServerServiceServer
}

func (g gRPCServer) GetSQRT(ctx context.Context, request *pb.GetSQRTRequest) (*pb.GetSQRTResponse, error) {
	number := float64(request.Number)
	result := heron(number, delta)
	g.log.Info("GetSQRT", slog.Float64("number", number), slog.Float64("result", result))
	return &pb.GetSQRTResponse{
		Result: float32(result),
	}, nil
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
	rest := flag.Bool("rest", false, "Enable REST API")
	gRPC := flag.Bool("grpc", false, "Enable gRPC")
	xDS := flag.Bool("xds", false, "Enable xds")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	eg, ctx := errgroup.WithContext(ctx)

	if *gRPC || *rest {
		eg.Go(runGRPCServer(ctx, logger))
	}

	if *rest {
		eg.Go(runRESTServer(ctx, logger))
	}

	if *xDS {
		eg.Go(runXDSGRPCServer(ctx, logger))
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

func runGRPCServer(ctx context.Context, log *slog.Logger) func() error {
	return func() error {
		serverMetrics := grpcPrometheus.NewServerMetrics(
			grpcPrometheus.WithServerHandlingTimeHistogram(
				grpcPrometheus.WithHistogramBuckets([]float64{0.001, 0.01, 0.1, 0.3, 0.6, 1, 3, 6, 9, 20, 30, 60, 90, 120}),
			),
		)
		prometheus.MustRegister(serverMetrics)

		opts := []grpc.ServerOption{
			grpc.ChainUnaryInterceptor(serverMetrics.UnaryServerInterceptor()),
		}

		server := grpc.NewServer(opts...)
		gServer := &gRPCServer{
			log: log,
		}
		pb.RegisterServerServiceServer(server, gServer)

		lis, err := net.Listen("tcp", grpcAddress)
		if err != nil {
			return err
		}
		go func() {
			if err := server.Serve(lis); err != nil {
				log.Error("running server", err)
			}
		}()

		log.Info("Running gRPC server")
		<-ctx.Done()
		log.Info("Shutting down gRPC server")

		server.GracefulStop()
		return nil
	}
}

func runXDSGRPCServer(ctx context.Context, log *slog.Logger) func() error {
	return func() error {
		serverMetrics := grpcPrometheus.NewServerMetrics()
		prometheus.MustRegister(serverMetrics)

		opts := []grpc.ServerOption{
			grpc.Creds(insecure.NewCredentials()),
			grpc.ChainUnaryInterceptor(serverMetrics.UnaryServerInterceptor()),
		}

		server, err := xds.NewGRPCServer(opts...)
		if err != nil {
			return err
		}
		gServer := &gRPCServer{
			log: log,
		}
		pb.RegisterServerServiceServer(server, gServer)

		lis, err := net.Listen("tcp", xDSAddress)
		if err != nil {
			return err
		}
		go func() {
			if err := server.Serve(lis); err != nil {
				log.Error("running server", err)
			}
		}()

		log.Info("Running gRPC server")
		<-ctx.Done()
		log.Info("Shutting down gRPC server")

		server.GracefulStop()
		return nil
	}
}

func runRESTServer(ctx context.Context, log *slog.Logger) func() error {
	return func() error {
		mux := runtime.NewServeMux()
		opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
		if err := pb.RegisterServerServiceHandlerFromEndpoint(ctx, mux, grpcAddress, opts); err != nil {
			return err
		}

		chiMux := chi.NewRouter()
		chiMux.Use(middleware.Recoverer)
		chiMux.Handle("/api/*", mux)

		server := &http.Server{
			Addr:              restAddress,
			Handler:           chiMux,
			ReadHeaderTimeout: time.Second * 5,
			WriteTimeout:      time.Second * 5,
			ReadTimeout:       time.Second * 5,
		}

		go func() {
			if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("running server", err)
			}
		}()

		log.Info("Running REST server")
		<-ctx.Done()
		log.Info("Shutting down REST server")

		if err := server.Shutdown(ctx); err != nil {
			return err
		}
		return nil
	}
}

func heron(number float64, delta float64) float64 {
	sqrt := number

	for math.Abs(sqrt*sqrt-number) > delta {
		sqrt = (sqrt + number/sqrt) / 2
	}

	return sqrt
}
