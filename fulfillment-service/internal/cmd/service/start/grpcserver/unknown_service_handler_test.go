/*
Copyright (c) 2026 Red Hat Inc.

Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with the
License. You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on an
"AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the License for the specific
language governing permissions and limitations under the License.
*/

package grpcserver

import (
	"context"
	"net"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/osac-project/osac/fulfillment-service/internal/services"
)

func newTestCounter(reg *prometheus.Registry) *prometheus.CounterVec {
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "fulfillment_disabled_service_requests_total",
	}, []string{"service", "method"})
	reg.MustRegister(counter)
	return counter
}

func startTestServer(t *testing.T, handler grpc.StreamHandler) *grpc.ClientConn {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	srv := grpc.NewServer(grpc.UnknownServiceHandler(handler))
	t.Cleanup(srv.Stop)
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func invokeMethod(conn *grpc.ClientConn, fullMethod string) error {
	return conn.Invoke(context.Background(), fullMethod, nil, &struct{}{})
}

func getCounterValue(counter *prometheus.CounterVec, labels ...string) float64 {
	m := &dto.Metric{}
	c, err := counter.GetMetricWithLabelValues(labels...)
	if err != nil {
		return 0
	}
	_ = c.Write(m)
	if m.Counter == nil {
		return 0
	}
	return *m.Counter.Value
}

func TestUnknownServiceHandler_DisabledService(t *testing.T) {
	reg := prometheus.NewRegistry()
	counter := newTestCounter(reg)
	flags := &services.Flags{CaaS: false, VMaaS: true, BMaaS: true, MaaS: false}
	handler := NewUnknownServiceHandler(flags, counter)
	conn := startTestServer(t, handler)

	err := invokeMethod(conn, "/osac.public.v1.Clusters/List")
	if err == nil {
		t.Fatal("expected error for disabled CaaS service")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.Unavailable {
		t.Errorf("expected Unavailable, got %v", st.Code())
	}
	if got := st.Message(); got != "the CaaS service is not enabled on this server" {
		t.Errorf("unexpected message: %s", got)
	}
}

func TestUnknownServiceHandler_UnknownService(t *testing.T) {
	reg := prometheus.NewRegistry()
	counter := newTestCounter(reg)
	flags := &services.Flags{CaaS: true, VMaaS: true, BMaaS: true, MaaS: true}
	handler := NewUnknownServiceHandler(flags, counter)
	conn := startTestServer(t, handler)

	err := invokeMethod(conn, "/osac.public.v1.NonExistent/Get")
	if err == nil {
		t.Fatal("expected error for unknown service")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.Unimplemented {
		t.Errorf("expected Unimplemented, got %v", st.Code())
	}
}

func TestUnknownServiceHandler_PrometheusCounter(t *testing.T) {
	reg := prometheus.NewRegistry()
	counter := newTestCounter(reg)
	flags := &services.Flags{CaaS: false, VMaaS: true, BMaaS: false, MaaS: false}
	handler := NewUnknownServiceHandler(flags, counter)
	conn := startTestServer(t, handler)

	_ = invokeMethod(conn, "/osac.public.v1.Clusters/List")
	_ = invokeMethod(conn, "/osac.public.v1.Clusters/Get")
	_ = invokeMethod(conn, "/osac.private.v1.BareMetalInstances/List")

	caasCount := getCounterValue(counter, "CaaS", "/osac.public.v1.Clusters/List")
	if caasCount != 1 {
		t.Errorf("expected CaaS List counter = 1, got %v", caasCount)
	}
	caasGetCount := getCounterValue(counter, "CaaS", "/osac.public.v1.Clusters/Get")
	if caasGetCount != 1 {
		t.Errorf("expected CaaS Get counter = 1, got %v", caasGetCount)
	}
	bmaasCount := getCounterValue(counter, "BMaaS", "/osac.private.v1.BareMetalInstances/List")
	if bmaasCount != 1 {
		t.Errorf("expected BMaaS counter = 1, got %v", bmaasCount)
	}
}

func TestUnknownServiceHandler_EnabledServiceNotBlocked(t *testing.T) {
	reg := prometheus.NewRegistry()
	counter := newTestCounter(reg)
	flags := &services.Flags{CaaS: false, VMaaS: true, BMaaS: true, MaaS: false}
	handler := NewUnknownServiceHandler(flags, counter)
	conn := startTestServer(t, handler)

	err := invokeMethod(conn, "/osac.public.v1.ComputeInstances/List")
	if err == nil {
		t.Fatal("expected error (service not actually registered)")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.Unimplemented {
		t.Errorf("expected Unimplemented for enabled-but-not-registered service, got %v", st.Code())
	}
}

func TestBuildDisabledServiceMap(t *testing.T) {
	t.Run("all enabled means empty map", func(t *testing.T) {
		m := buildDisabledServiceMap(&services.Flags{CaaS: true, VMaaS: true, BMaaS: true, MaaS: true})
		if len(m) != 0 {
			t.Errorf("expected empty map, got %d entries", len(m))
		}
	})

	t.Run("all disabled populates all prefixes", func(t *testing.T) {
		m := buildDisabledServiceMap(&services.Flags{CaaS: false, VMaaS: false, BMaaS: false, MaaS: false})
		totalPrefixes := 0
		for _, prefixes := range disabledServicePrefixes {
			totalPrefixes += len(prefixes)
		}
		if len(m) != totalPrefixes {
			t.Errorf("expected %d entries, got %d", totalPrefixes, len(m))
		}
	})

	t.Run("partial disable only includes disabled groups", func(t *testing.T) {
		m := buildDisabledServiceMap(&services.Flags{CaaS: true, VMaaS: false, BMaaS: true, MaaS: false})
		for prefix := range m {
			if group := m[prefix]; group != "VMaaS" {
				t.Errorf("expected only VMaaS in disabled map, got %s for %s", group, prefix)
			}
		}
		if len(m) != len(disabledServicePrefixes["VMaaS"]) {
			t.Errorf("expected %d VMaaS entries, got %d", len(disabledServicePrefixes["VMaaS"]), len(m))
		}
	})
}
