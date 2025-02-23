/*
 * Copyright 2018- The Pixie Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 * SPDX-License-Identifier: Apache-2.0
 */

package main

import (
	"net/http"
	_ "net/http/pprof"

	"github.com/gofrs/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/stan.go"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"google.golang.org/grpc"

	"px.dev/pixie/src/cloud/vzconn/bridge"
	"px.dev/pixie/src/cloud/vzconn/vzconnpb"
	"px.dev/pixie/src/cloud/vzmgr/vzmgrpb"
	"px.dev/pixie/src/shared/services"
	"px.dev/pixie/src/shared/services/env"
	"px.dev/pixie/src/shared/services/healthz"
	"px.dev/pixie/src/shared/services/server"
)

func init() {
	pflag.String("vzmgr_service", "kubernetes:///vzmgr-service.plc:51800", "The profile service url (load balancer/list is ok)")
	pflag.String("nats_url", "pl-nats", "The URL of NATS")
	pflag.String("stan_cluster", "pl-stan", "The name of the STAN cluster")
	pflag.String("domain_name", "dev.withpixie.dev", "The domain name of Pixie Cloud")
}

func newVZMgrClients() (vzmgrpb.VZMgrServiceClient, vzmgrpb.VZDeploymentServiceClient, error) {
	dialOpts, err := services.GetGRPCClientDialOpts()
	if err != nil {
		return nil, nil, err
	}

	vzmgrChannel, err := grpc.Dial(viper.GetString("vzmgr_service"), dialOpts...)
	if err != nil {
		return nil, nil, err
	}

	return vzmgrpb.NewVZMgrServiceClient(vzmgrChannel), vzmgrpb.NewVZDeploymentServiceClient(vzmgrChannel), nil
}

func createStanNatsConnection(clientID string) (*nats.Conn, stan.Conn, error) {
	nc, err := nats.Connect(viper.GetString("nats_url"),
		nats.ClientCert(viper.GetString("client_tls_cert"), viper.GetString("client_tls_key")),
		nats.RootCAs(viper.GetString("tls_ca_cert")))
	if err != nil {
		log.WithError(err).Error("NATS connection failed")
		return nil, nil, err
	}
	sc, err := stan.Connect(viper.GetString("stan_cluster"),
		clientID, stan.NatsConn(nc),
		stan.SetConnectionLostHandler(func(_ stan.Conn, err error) {
			log.WithError(err).Fatal("STAN Connection Lost")
		}))
	if err != nil {
		log.WithError(err).Error("STAN connection failed")
		return nil, nil, err
	}
	return nc, sc, nil
}

func main() {
	services.SetupService("vzconn-service", 51600)
	services.PostFlagSetupAndParse()
	services.CheckServiceFlags()
	services.SetupServiceLogging()

	mux := http.NewServeMux()
	// This handles all the pprof endpoints.
	mux.Handle("/debug/", http.DefaultServeMux)
	healthz.RegisterDefaultChecks(mux)
	// VZConn is the backend for a GCLB and that health checks on "/" instead of the regular health check endpoint.
	healthz.InstallPathHandler(mux, "/")

	// Communication from Vizier to VZConn is not auth'd via GRPC auth.
	serverOpts := &server.GRPCServerOptions{
		DisableAuth: map[string]bool{
			"/px.services.VZConnService/NATSBridge":               true,
			"/px.services.VZConnService/RegisterVizierDeployment": true,
		},
	}

	s := server.NewPLServerWithOptions(env.New(viper.GetString("domain_name")), mux, serverOpts)
	nc, sc, err := createStanNatsConnection(uuid.Must(uuid.NewV4()).String())
	if err != nil {
		log.WithError(err).Fatal("Could not connect to NATS/STAN")
	}

	nc.SetErrorHandler(func(conn *nats.Conn, subscription *nats.Subscription, err error) {
		log.WithField("Sub", subscription.Subject).
			WithError(err).
			Error("Error with NATS handler")
	})

	vzmgrClient, vzdeployClient, err := newVZMgrClients()
	if err != nil {
		log.WithError(err).Fatal("failed to initialize vizer manager RPC client")
		panic(err)
	}
	svr := bridge.NewBridgeGRPCServer(vzmgrClient, vzdeployClient, nc, sc)
	vzconnpb.RegisterVZConnServiceServer(s.GRPCServer(), svr)

	s.Start()
	s.StopOnInterrupt()
}
