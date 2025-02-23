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

package controller_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dgrijalva/jwt-go/v4"
	"github.com/gofrs/uuid"
	"github.com/gogo/protobuf/proto"
	"github.com/gogo/protobuf/types"
	bindata "github.com/golang-migrate/migrate/source/go_bindata"
	"github.com/golang/mock/gomock"
	"github.com/jmoiron/sqlx"
	"github.com/nats-io/nats.go"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"px.dev/pixie/src/api/proto/uuidpb"
	"px.dev/pixie/src/cloud/dnsmgr/dnsmgrpb"
	mock_dnsmgrpb "px.dev/pixie/src/cloud/dnsmgr/dnsmgrpb/mock"
	"px.dev/pixie/src/cloud/shared/messagespb"
	"px.dev/pixie/src/cloud/vzmgr/controller"
	mock_controller "px.dev/pixie/src/cloud/vzmgr/controller/mock"
	"px.dev/pixie/src/cloud/vzmgr/schema"
	"px.dev/pixie/src/cloud/vzmgr/vzerrors"
	"px.dev/pixie/src/cloud/vzmgr/vzmgrpb"
	"px.dev/pixie/src/shared/cvmsgspb"
	"px.dev/pixie/src/shared/k8s/metadatapb"
	"px.dev/pixie/src/shared/services/authcontext"
	"px.dev/pixie/src/shared/services/pgtest"
	jwtutils "px.dev/pixie/src/shared/services/utils"
	"px.dev/pixie/src/utils"
	"px.dev/pixie/src/utils/testingutils"
)

var (
	testAuthOrgID                   = "223e4567-e89b-12d3-a456-426655440000"
	testNonAuthOrgID                = "223e4567-e89b-12d3-a456-426655440001"
	testProjectName                 = "foo"
	testDisconnectedClusterEmptyUID = "123e4567-e89b-12d3-a456-426655440003"
	testExistingCluster             = "823e4567-e89b-12d3-a456-426655440008"
	testExistingClusterActive       = "923e4567-e89b-12d3-a456-426655440008"
)

var testPodStatuses controller.PodStatuses = map[string]*cvmsgspb.PodStatus{
	"vizier-proxy": {
		Name:   "vizier-proxy",
		Status: metadatapb.RUNNING,
	},
	"vizier-query-broker": {
		Name:   "vizier-query-broker",
		Status: metadatapb.RUNNING,
	},
}

func TestMain(m *testing.M) {
	err := testMain(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Got error: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

var db *sqlx.DB

func testMain(m *testing.M) error {
	s := bindata.Resource(schema.AssetNames(), schema.Asset)
	testDB, teardown, err := pgtest.SetupTestDB(s)
	if err != nil {
		return fmt.Errorf("failed to start test database: %w", err)
	}

	defer teardown()
	db = testDB

	if c := m.Run(); c != 0 {
		return fmt.Errorf("some tests failed with code: %d", c)
	}
	return nil
}

func mustLoadTestData(db *sqlx.DB) {
	db.MustExec(`DELETE FROM vizier_cluster_info`)
	db.MustExec(`DELETE FROM vizier_cluster`)

	insertCluster := `INSERT INTO vizier_cluster(org_id, id, project_name, cluster_uid, cluster_version, cluster_name) VALUES ($1, $2, $3, $4, $5, $6)`
	db.MustExec(insertCluster, testAuthOrgID, "123e4567-e89b-12d3-a456-426655440000", testProjectName, "k8sID", "", "unknown_cluster")
	db.MustExec(insertCluster, testAuthOrgID, "123e4567-e89b-12d3-a456-426655440001", testProjectName, "cUID", "cVers", "healthy_cluster")
	db.MustExec(insertCluster, testAuthOrgID, "123e4567-e89b-12d3-a456-426655440002", testProjectName, "k8s2", "", "unhealthy_cluster")
	db.MustExec(insertCluster, testAuthOrgID, testDisconnectedClusterEmptyUID, testProjectName, "", "", "disconnected_cluster")
	db.MustExec(insertCluster, testAuthOrgID, testExistingCluster, testProjectName, "existing_cluster", "", "test_cluster_1234")
	db.MustExec(insertCluster, testAuthOrgID, testExistingClusterActive, testProjectName, "my_other_cluster", "", "existing_cluster")
	db.MustExec(insertCluster, testNonAuthOrgID, "223e4567-e89b-12d3-a456-426655440003", testProjectName, "k8s5", "", "non_auth_1")
	db.MustExec(insertCluster, testNonAuthOrgID, "323e4567-e89b-12d3-a456-426655440003", testProjectName, "k8s6", "", "non_auth_2")

	insertClusterInfo := `INSERT INTO vizier_cluster_info(vizier_cluster_id, status, address, jwt_signing_key, last_heartbeat,
						  passthrough_enabled, auto_update_enabled, vizier_version, control_plane_pod_statuses, num_nodes, num_instrumented_nodes)
						  VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`
	db.MustExec(insertClusterInfo, "123e4567-e89b-12d3-a456-426655440000", "UNKNOWN", "addr0",
		"key0", "2011-05-16 15:36:38", true, false, "", testPodStatuses, 10, 8)
	db.MustExec(insertClusterInfo, "123e4567-e89b-12d3-a456-426655440001", "HEALTHY", "addr1",
		"\\xc30d04070302c5374a5098262b6d7bd23f01822f741dbebaa680b922b55fd16eb985aeb09505f8fc4a36f0e11ebb8e18f01f684146c761e2234a81e50c21bca2907ea37736f2d9a5834997f4dd9e288c",
		"2011-05-17 15:36:38", false, true, "vzVers", "{}", 12, 9)
	db.MustExec(insertClusterInfo, "123e4567-e89b-12d3-a456-426655440002", "UNHEALTHY", "addr2", "key2", "2011-05-18 15:36:38",
		true, false, "", "{}", 4, 4)
	db.MustExec(insertClusterInfo, testDisconnectedClusterEmptyUID, "DISCONNECTED", "addr3", "key3", "2011-05-19 15:36:38",
		false, true, "", "{}", 3, 2)
	db.MustExec(insertClusterInfo, testExistingCluster, "DISCONNECTED", "addr3", "key3", "2011-05-19 15:36:38",
		false, true, "", "{}", 5, 4)
	db.MustExec(insertClusterInfo, testExistingClusterActive, "UNHEALTHY", "addr3", "key3", "2011-05-19 15:36:38",
		false, true, "", "{}", 10, 4)
	db.MustExec(insertClusterInfo, "223e4567-e89b-12d3-a456-426655440003", "HEALTHY", "addr3", "key3", "2011-05-19 15:36:38",
		true, true, "", "{}", 2, 0)
	db.MustExec(insertClusterInfo, "323e4567-e89b-12d3-a456-426655440003", "HEALTHY", "addr3", "key3", "2011-05-19 15:36:38",
		false, true, "", "{}", 4, 2)

	db.MustExec(`UPDATE vizier_cluster SET cluster_name=NULL WHERE id=$1`, testDisconnectedClusterEmptyUID)
}

func CreateTestContext() context.Context {
	sCtx := authcontext.New()
	sCtx.Claims = jwtutils.GenerateJWTForUser("abcdef", testAuthOrgID, "test@test.com", time.Now(), "pixie")
	return authcontext.NewContext(context.Background(), sCtx)
}

func TestServer_GetViziersByOrg(t *testing.T) {
	mustLoadTestData(db)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockDNSClient := mock_dnsmgrpb.NewMockDNSMgrServiceClient(ctrl)

	s := controller.New(db, "test", mockDNSClient, nil, nil)

	t.Run("valid", func(t *testing.T) {
		// Fetch the test data that was inserted earlier.
		resp, err := s.GetViziersByOrg(CreateTestContext(), utils.ProtoFromUUIDStrOrNil(testAuthOrgID))
		require.NoError(t, err)
		require.NotNil(t, resp)

		assert.Equal(t, len(resp.VizierIDs), 6)

		// Convert to UUID string array.
		var ids []string
		for _, val := range resp.VizierIDs {
			ids = append(ids, utils.UUIDFromProtoOrNil(val).String())
		}
		sort.Strings(ids)
		expected := []string{
			"123e4567-e89b-12d3-a456-426655440000",
			"123e4567-e89b-12d3-a456-426655440001",
			"123e4567-e89b-12d3-a456-426655440002",
			"123e4567-e89b-12d3-a456-426655440003",
			"823e4567-e89b-12d3-a456-426655440008",
			"923e4567-e89b-12d3-a456-426655440008",
		}
		sort.Strings(expected)
		assert.Equal(t, ids, expected)
	})

	t.Run("No such org id", func(t *testing.T) {
		resp, err := s.GetViziersByOrg(CreateTestContext(), utils.ProtoFromUUIDStrOrNil("323e4567-e89b-12d3-a456-426655440000"))
		require.NotNil(t, err)
		assert.Nil(t, resp)
		assert.Equal(t, status.Code(err), codes.PermissionDenied)
	})

	t.Run("bad input org id", func(t *testing.T) {
		resp, err := s.GetViziersByOrg(CreateTestContext(), utils.ProtoFromUUIDStrOrNil("3e4567-e89b-12d3-a456-426655440000"))
		require.NotNil(t, err)
		assert.Nil(t, resp)
		assert.Equal(t, status.Code(err), codes.InvalidArgument)
	})

	t.Run("missing input org id", func(t *testing.T) {
		resp, err := s.GetViziersByOrg(CreateTestContext(), nil)
		require.NotNil(t, err)
		assert.Nil(t, resp)
		assert.Equal(t, status.Code(err), codes.InvalidArgument)
	})

	t.Run("mismatched input org id", func(t *testing.T) {
		resp, err := s.GetViziersByOrg(CreateTestContext(), utils.ProtoFromUUIDStrOrNil(testNonAuthOrgID))
		require.NotNil(t, err)
		assert.Nil(t, resp)
		assert.Equal(t, status.Code(err), codes.PermissionDenied)
	})
}

func TestServer_GetVizierInfo(t *testing.T) {
	mustLoadTestData(db)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockDNSClient := mock_dnsmgrpb.NewMockDNSMgrServiceClient(ctrl)

	s := controller.New(db, "test", mockDNSClient, nil, nil)
	resp, err := s.GetVizierInfo(CreateTestContext(), utils.ProtoFromUUIDStrOrNil("123e4567-e89b-12d3-a456-426655440001"))
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, resp.VizierID, utils.ProtoFromUUIDStrOrNil("123e4567-e89b-12d3-a456-426655440001"))
	assert.Equal(t, resp.Status, cvmsgspb.VZ_ST_HEALTHY)
	assert.Greater(t, resp.LastHeartbeatNs, int64(0))
	assert.Equal(t, resp.Config.PassthroughEnabled, false)
	assert.Equal(t, resp.Config.AutoUpdateEnabled, true)
	assert.Equal(t, "vzVers", resp.VizierVersion)
	assert.Equal(t, "cVers", resp.ClusterVersion)
	assert.Equal(t, "healthy_cluster", resp.ClusterName)
	assert.Equal(t, "cUID", resp.ClusterUID)
	assert.Equal(t, int32(12), resp.NumNodes)
	assert.Equal(t, int32(9), resp.NumInstrumentedNodes)

	// Test that the empty pods list case works.
	assert.Equal(t, make(controller.PodStatuses), controller.PodStatuses(resp.ControlPlanePodStatuses))
	// Test that the non-empty pods list case works.
	resp, err = s.GetVizierInfo(CreateTestContext(), utils.ProtoFromUUIDStrOrNil("123e4567-e89b-12d3-a456-426655440000"))
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, testPodStatuses, controller.PodStatuses(resp.ControlPlanePodStatuses))
}

func TestServer_GetVizierInfos(t *testing.T) {
	mustLoadTestData(db)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockDNSClient := mock_dnsmgrpb.NewMockDNSMgrServiceClient(ctrl)

	requestedIDs := []*uuidpb.UUID{
		utils.ProtoFromUUIDStrOrNil("123e4567-e89b-12d3-a456-426655440001"), // Vizier from current org.
		utils.ProtoFromUUIDStrOrNil("223e4567-e89b-12d3-a456-426655440003"), // Vizier from another org.
		utils.ProtoFromUUIDStrOrNil("223e4567-e89b-12d3-a456-426655440012"), // Invalid vizier ID.
		utils.ProtoFromUUIDStrOrNil("123e4567-e89b-12d3-a456-426655440000"), // Vizier from current org.
	}

	s := controller.New(db, "test", mockDNSClient, nil, nil)
	resp, err := s.GetVizierInfos(CreateTestContext(), &vzmgrpb.GetVizierInfosRequest{
		VizierIDs: requestedIDs,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, 4, len(resp.VizierInfos))
	assert.Equal(t, utils.ProtoFromUUIDStrOrNil("123e4567-e89b-12d3-a456-426655440001"), resp.VizierInfos[0].VizierID)
	assert.Equal(t, "vzVers", resp.VizierInfos[0].VizierVersion)
	assert.Equal(t, &cvmsgspb.VizierInfo{}, resp.VizierInfos[1])
	assert.Equal(t, &cvmsgspb.VizierInfo{}, resp.VizierInfos[2])
	assert.Equal(t, utils.ProtoFromUUIDStrOrNil("123e4567-e89b-12d3-a456-426655440000"), resp.VizierInfos[3].VizierID)
	assert.Equal(t, "k8sID", resp.VizierInfos[3].ClusterUID)
}

func TestServer_UpdateVizierConfig(t *testing.T) {
	mustLoadTestData(db)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockDNSClient := mock_dnsmgrpb.NewMockDNSMgrServiceClient(ctrl)

	s := controller.New(db, "test", mockDNSClient, nil, nil)
	vzIDpb := utils.ProtoFromUUIDStrOrNil("123e4567-e89b-12d3-a456-426655440001")
	resp, err := s.UpdateVizierConfig(CreateTestContext(), &cvmsgspb.UpdateVizierConfigRequest{
		VizierID: vzIDpb,
		ConfigUpdate: &cvmsgspb.VizierConfigUpdate{
			PassthroughEnabled: &types.BoolValue{Value: true},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Check that the value was actually updated.
	infoResp, err := s.GetVizierInfo(CreateTestContext(), vzIDpb)
	require.NoError(t, err)
	require.NotNil(t, infoResp)
	assert.Equal(t, infoResp.Config.PassthroughEnabled, true)
}

func TestServer_UpdateVizierConfig_AutoUpdate(t *testing.T) {
	mustLoadTestData(db)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockDNSClient := mock_dnsmgrpb.NewMockDNSMgrServiceClient(ctrl)

	s := controller.New(db, "test", mockDNSClient, nil, nil)
	vzIDpb := utils.ProtoFromUUIDStrOrNil("123e4567-e89b-12d3-a456-426655440001")

	_, err := s.UpdateVizierConfig(CreateTestContext(), &cvmsgspb.UpdateVizierConfigRequest{
		VizierID: vzIDpb,
		ConfigUpdate: &cvmsgspb.VizierConfigUpdate{
			AutoUpdateEnabled: &types.BoolValue{Value: false},
		},
	})
	require.NotNil(t, err)
	assert.Equal(t, status.Code(err), codes.InvalidArgument)
}

func TestServer_UpdateVizierConfig_WrongOrg(t *testing.T) {
	mustLoadTestData(db)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockDNSClient := mock_dnsmgrpb.NewMockDNSMgrServiceClient(ctrl)

	s := controller.New(db, "test", mockDNSClient, nil, nil)
	resp, err := s.UpdateVizierConfig(CreateTestContext(), &cvmsgspb.UpdateVizierConfigRequest{
		VizierID: utils.ProtoFromUUIDStrOrNil("223e4567-e89b-12d3-a456-426655440003"),
		ConfigUpdate: &cvmsgspb.VizierConfigUpdate{
			PassthroughEnabled: &types.BoolValue{Value: true},
		},
	})
	require.Nil(t, resp)
	require.NotNil(t, err)
	assert.Equal(t, status.Code(err), codes.NotFound)
}

func TestServer_UpdateVizierConfig_NoUpdates(t *testing.T) {
	mustLoadTestData(db)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockDNSClient := mock_dnsmgrpb.NewMockDNSMgrServiceClient(ctrl)

	s := controller.New(db, "test", mockDNSClient, nil, nil)
	resp, err := s.UpdateVizierConfig(CreateTestContext(), &cvmsgspb.UpdateVizierConfigRequest{
		VizierID:     utils.ProtoFromUUIDStrOrNil("123e4567-e89b-12d3-a456-426655440001"),
		ConfigUpdate: &cvmsgspb.VizierConfigUpdate{},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Check that the value was not updated.
	infoResp, err := s.GetVizierInfo(CreateTestContext(), utils.ProtoFromUUIDStrOrNil("123e4567-e89b-12d3-a456-426655440001"))
	require.NoError(t, err)
	require.NotNil(t, infoResp)
	assert.Equal(t, infoResp.Config.PassthroughEnabled, false)
}

func TestServer_GetVizierConnectionInfo(t *testing.T) {
	mustLoadTestData(db)
	viper.Set("domain_name", "withpixie.ai")

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockDNSClient := mock_dnsmgrpb.NewMockDNSMgrServiceClient(ctrl)

	s := controller.New(db, "test", mockDNSClient, nil, nil)
	resp, err := s.GetVizierConnectionInfo(CreateTestContext(), utils.ProtoFromUUIDStrOrNil("123e4567-e89b-12d3-a456-426655440001"))
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, resp.IPAddress, "https://addr1")
	assert.NotNil(t, resp.Token)
	claims := jwt.MapClaims{}
	_, err = jwt.ParseWithClaims(resp.Token, claims, func(token *jwt.Token) (interface{}, error) {
		return []byte("key0"), nil
	}, jwt.WithAudience("vizier"))
	require.NoError(t, err)
	assert.Equal(t, "cluster", claims["Scopes"].(string))
}

func TestServer_VizierConnectedUnhealthy(t *testing.T) {
	mustLoadTestData(db)

	nc, cleanup := testingutils.MustStartTestNATS(t)
	defer cleanup()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockDNSClient := mock_dnsmgrpb.NewMockDNSMgrServiceClient(ctrl)

	s := controller.New(db, "test", mockDNSClient, nc, nil)
	req := &cvmsgspb.RegisterVizierRequest{
		VizierID: utils.ProtoFromUUIDStrOrNil("123e4567-e89b-12d3-a456-426655440001"),
		JwtKey:   "the-token",
		ClusterInfo: &cvmsgspb.VizierClusterInfo{
			ClusterUID: "cUID",
		},
	}

	resp, err := s.VizierConnected(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, resp.Status, cvmsgspb.ST_OK)

	// Check to make sure DB insert for JWT signing key is correct.
	clusterQuery := `SELECT PGP_SYM_DECRYPT(jwt_signing_key::bytea, 'test') as jwt_signing_key, status from vizier_cluster_info WHERE vizier_cluster_id=$1`

	var clusterInfo struct {
		JWTSigningKey string `db:"jwt_signing_key"`
		Status        string `db:"status"`
	}
	clusterID, err := uuid.FromString("123e4567-e89b-12d3-a456-426655440001")
	require.NoError(t, err)
	err = db.Get(&clusterInfo, clusterQuery, clusterID)
	require.NoError(t, err)
	assert.Equal(t, "the-token", clusterInfo.JWTSigningKey[controller.SaltLength:])

	assert.Equal(t, "UNHEALTHY", clusterInfo.Status)
	// TODO(zasgar): write more tests here.
}

func TestServer_VizierConnectedHealthy(t *testing.T) {
	mustLoadTestData(db)

	nc, cleanup := testingutils.MustStartTestNATS(t)
	defer cleanup()

	subCh := make(chan *nats.Msg, 1)
	natsSub, err := nc.ChanSubscribe("VizierConnected", subCh)
	require.NoError(t, err)
	defer func() {
		err = natsSub.Unsubscribe()
		require.NoError(t, err)
	}()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockDNSClient := mock_dnsmgrpb.NewMockDNSMgrServiceClient(ctrl)

	s := controller.New(db, "test", mockDNSClient, nc, nil)
	req := &cvmsgspb.RegisterVizierRequest{
		VizierID: utils.ProtoFromUUIDStrOrNil("123e4567-e89b-12d3-a456-426655440001"),
		JwtKey:   "the-token",
		Address:  "127.0.0.1",
		ClusterInfo: &cvmsgspb.VizierClusterInfo{
			ClusterUID:     "cUID",
			ClusterVersion: "1234",
			VizierVersion:  "some version",
		},
	}

	resp, err := s.VizierConnected(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, resp.Status, cvmsgspb.ST_OK)

	// Check to make sure DB insert for JWT signing key is correct.
	clusterQuery := `SELECT PGP_SYM_DECRYPT(jwt_signing_key::bytea, 'test') as jwt_signing_key, status, vizier_version from vizier_cluster_info WHERE vizier_cluster_id=$1`

	var clusterInfo struct {
		JWTSigningKey string `db:"jwt_signing_key"`
		Status        string `db:"status"`
		VizierVersion string `db:"vizier_version"`
	}
	clusterID, err := uuid.FromString("123e4567-e89b-12d3-a456-426655440001")
	require.NoError(t, err)
	err = db.Get(&clusterInfo, clusterQuery, clusterID)
	require.NoError(t, err)
	assert.Equal(t, "CONNECTED", clusterInfo.Status)
	assert.Equal(t, "some version", clusterInfo.VizierVersion)

	select {
	case msg := <-subCh:
		req := &messagespb.VizierConnected{}
		err := proto.Unmarshal(msg.Data, req)
		require.NoError(t, err)
		assert.Equal(t, "cUID", req.K8sUID)
		assert.Equal(t, "223e4567-e89b-12d3-a456-426655440000", utils.UUIDFromProtoOrNil(req.OrgID).String())
		assert.Equal(t, "123e4567-e89b-12d3-a456-426655440001", utils.UUIDFromProtoOrNil(req.VizierID).String())
		return
	case <-time.After(1 * time.Second):
		t.Fatal("Timed out")
		return
	}
	// TODO(zasgar): write more tests here.
}

func TestServer_HandleVizierHeartbeat(t *testing.T) {
	mustLoadTestData(db)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockDNSClient := mock_dnsmgrpb.NewMockDNSMgrServiceClient(ctrl)

	nc, cleanup := testingutils.MustStartTestNATS(t)
	defer cleanup()

	updater := mock_controller.NewMockVzUpdater(ctrl)
	s := controller.New(db, "test", mockDNSClient, nc, updater)

	tests := []struct {
		name                       string
		expectGetDNSAddressCalled  bool
		dnsAddressResponse         *dnsmgrpb.GetDNSAddressResponse
		dnsAddressError            error
		vizierID                   string
		hbAddress                  string
		hbPort                     int
		updatedClusterStatus       string
		expectedClusterAddress     string
		status                     cvmsgspb.VizierStatus
		bootstrap                  bool
		bootstrapVersion           string
		expectDeploy               bool
		expectedFetchVizierVersion bool
		controlPlanePodStatuses    controller.PodStatuses
		numNodes                   int32
		numInstrumentedNodes       int32
		checkVersion               bool
		checkDB                    bool
		versionUpdated             bool
		disableAutoUpdate          bool
	}{
		{
			name:                      "valid vizier",
			expectGetDNSAddressCalled: true,
			dnsAddressResponse: &dnsmgrpb.GetDNSAddressResponse{
				DNSAddress: "abc.clusters.dev.withpixie.dev",
			},
			dnsAddressError:         nil,
			vizierID:                "123e4567-e89b-12d3-a456-426655440001",
			hbAddress:               "127.0.0.1",
			hbPort:                  123,
			updatedClusterStatus:    "HEALTHY",
			expectedClusterAddress:  "abc.clusters.dev.withpixie.dev:123",
			controlPlanePodStatuses: testPodStatuses,
			numNodes:                4,
			numInstrumentedNodes:    3,
			checkVersion:            true,
			checkDB:                 true,
		},
		{
			name:                      "valid vizier, no autoupdate",
			expectGetDNSAddressCalled: true,
			dnsAddressResponse: &dnsmgrpb.GetDNSAddressResponse{
				DNSAddress: "abc.clusters.dev.withpixie.dev",
			},
			dnsAddressError:         nil,
			vizierID:                "123e4567-e89b-12d3-a456-426655440001",
			hbAddress:               "127.0.0.1",
			hbPort:                  123,
			updatedClusterStatus:    "HEALTHY",
			expectedClusterAddress:  "abc.clusters.dev.withpixie.dev:123",
			controlPlanePodStatuses: testPodStatuses,
			numNodes:                4,
			numInstrumentedNodes:    3,
			checkVersion:            false,
			checkDB:                 true,
			disableAutoUpdate:       true,
		},
		{
			name:                      "valid vizier dns failed",
			expectGetDNSAddressCalled: true,
			dnsAddressResponse:        nil,
			dnsAddressError:           errors.New("Could not get DNS address"),
			vizierID:                  "123e4567-e89b-12d3-a456-426655440001",
			hbAddress:                 "127.0.0.1",
			hbPort:                    123,
			updatedClusterStatus:      "HEALTHY",
			expectedClusterAddress:    "127.0.0.1:123",
			numNodes:                  4,
			numInstrumentedNodes:      3,
			checkVersion:              true,
			checkDB:                   true,
			versionUpdated:            true,
		},
		{
			name:                      "valid vizier no address",
			expectGetDNSAddressCalled: false,
			vizierID:                  "123e4567-e89b-12d3-a456-426655440001",
			hbAddress:                 "",
			hbPort:                    0,
			updatedClusterStatus:      "UNHEALTHY",
			expectedClusterAddress:    "",
			numNodes:                  4,
			numInstrumentedNodes:      3,
			checkVersion:              true,
			checkDB:                   true,
		},
		{
			name:                      "unknown vizier",
			expectGetDNSAddressCalled: false,
			vizierID:                  "223e4567-e89b-12d3-a456-426655440001",
			hbAddress:                 "",
			updatedClusterStatus:      "",
			expectedClusterAddress:    "",
			status:                    cvmsgspb.VZ_ST_UPDATING,
			checkVersion:              false,
			checkDB:                   false,
			disableAutoUpdate:         true,
		},
		{
			name:                      "bootstrap vizier",
			expectGetDNSAddressCalled: true,
			dnsAddressResponse: &dnsmgrpb.GetDNSAddressResponse{
				DNSAddress: "abc.clusters.dev.withpixie.dev",
			},
			dnsAddressError:            nil,
			vizierID:                   "123e4567-e89b-12d3-a456-426655440001",
			hbAddress:                  "127.0.0.1",
			hbPort:                     123,
			updatedClusterStatus:       "UPDATING",
			expectedClusterAddress:     "abc.clusters.dev.withpixie.dev:123",
			status:                     cvmsgspb.VZ_ST_UPDATING,
			bootstrap:                  true,
			bootstrapVersion:           "",
			expectedFetchVizierVersion: true,
			expectDeploy:               true,
			numNodes:                   4,
			numInstrumentedNodes:       3,
			checkDB:                    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dnsMgrReq := &dnsmgrpb.GetDNSAddressRequest{
				ClusterID: utils.ProtoFromUUIDStrOrNil(tc.vizierID),
				IPAddress: tc.hbAddress,
			}

			if tc.expectGetDNSAddressCalled {
				mockDNSClient.EXPECT().
					GetDNSAddress(gomock.Any(), dnsMgrReq).
					Return(tc.dnsAddressResponse, tc.dnsAddressError)
			}

			if tc.bootstrap {
				updater.
					EXPECT().
					UpdateOrInstallVizier(uuid.FromStringOrNil(tc.vizierID), "", false).
					Return(&cvmsgspb.V2CMessage{}, nil)
			}

			if tc.checkVersion {
				updater.
					EXPECT().
					VersionUpToDate(gomock.Any()).
					Return(!tc.versionUpdated)
			}

			if tc.versionUpdated {
				updater.
					EXPECT().
					AddToUpdateQueue(uuid.FromStringOrNil(tc.vizierID))
			}

			nestedMsg := &cvmsgspb.VizierHeartbeat{
				VizierID:             utils.ProtoFromUUIDStrOrNil(tc.vizierID),
				Time:                 100,
				SequenceNumber:       200,
				Address:              tc.hbAddress,
				Port:                 int32(tc.hbPort),
				Status:               tc.status,
				BootstrapMode:        tc.bootstrap,
				BootstrapVersion:     tc.bootstrapVersion,
				PodStatuses:          tc.controlPlanePodStatuses,
				NumNodes:             tc.numNodes,
				NumInstrumentedNodes: tc.numInstrumentedNodes,
				DisableAutoUpdate:    tc.disableAutoUpdate,
			}
			nestedAny, err := types.MarshalAny(nestedMsg)
			if err != nil {
				t.Fatal("Could not marshal pb")
			}

			req := &cvmsgspb.V2CMessage{
				Msg: nestedAny,
			}

			s.HandleVizierHeartbeat(req)

			// Check database.
			clusterQuery := `
			SELECT status, address, control_plane_pod_statuses, num_nodes, num_instrumented_nodes, auto_update_enabled
			FROM vizier_cluster_info WHERE vizier_cluster_id=$1`
			var clusterInfo struct {
				Status                  string                 `db:"status"`
				Address                 string                 `db:"address"`
				ControlPlanePodStatuses controller.PodStatuses `db:"control_plane_pod_statuses"`
				NumNodes                int32                  `db:"num_nodes"`
				NumInstrumentedNodes    int32                  `db:"num_instrumented_nodes"`
				AutoUpdateEnabled       bool                   `db:"auto_update_enabled"`
			}
			clusterID, err := uuid.FromString(tc.vizierID)
			require.NoError(t, err)
			err = db.Get(&clusterInfo, clusterQuery, clusterID)
			if tc.checkDB {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.updatedClusterStatus, clusterInfo.Status)
			assert.Equal(t, tc.expectedClusterAddress, clusterInfo.Address)
			assert.Equal(t, tc.numNodes, clusterInfo.NumNodes)
			assert.Equal(t, tc.numInstrumentedNodes, clusterInfo.NumInstrumentedNodes)
			assert.Equal(t, !tc.disableAutoUpdate, clusterInfo.AutoUpdateEnabled)
		})
	}
}

func TestServer_GetSSLCerts(t *testing.T) {
	mustLoadTestData(db)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockDNSClient := mock_dnsmgrpb.NewMockDNSMgrServiceClient(ctrl)

	nc, cleanup := testingutils.MustStartTestNATS(t)
	defer cleanup()

	subCh := make(chan *nats.Msg, 1)
	sub, err := nc.ChanSubscribe("c2v.123e4567-e89b-12d3-a456-426655440001.sslResp", subCh)
	if err != nil {
		t.Fatal("Could not subscribe to NATS.")
	}
	defer func() {
		err = sub.Unsubscribe()
		require.NoError(t, err)
	}()

	s := controller.New(db, "test", mockDNSClient, nc, nil)

	t.Run("dnsmgr error", func(t *testing.T) {
		dnsMgrReq := &dnsmgrpb.GetSSLCertsRequest{
			ClusterID: utils.ProtoFromUUIDStrOrNil("123e4567-e89b-12d3-a456-426655440001"),
		}

		dnsMgrResp := &dnsmgrpb.GetSSLCertsResponse{
			Key:  "abcd",
			Cert: "efgh",
		}

		mockDNSClient.EXPECT().
			GetSSLCerts(gomock.Any(), dnsMgrReq).
			Return(dnsMgrResp, nil)

		nestedMsg := &cvmsgspb.VizierSSLCertRequest{
			VizierID: utils.ProtoFromUUIDStrOrNil("123e4567-e89b-12d3-a456-426655440001"),
		}
		nestedAny, err := types.MarshalAny(nestedMsg)
		if err != nil {
			t.Fatal("Could not marshal pb")
		}

		req := &cvmsgspb.V2CMessage{
			Msg: nestedAny,
		}

		s.HandleSSLRequest(req)

		// Listen for response.
		select {
		case m := <-subCh:
			pb := &cvmsgspb.C2VMessage{}
			err = proto.Unmarshal(m.Data, pb)
			if err != nil {
				t.Fatal("Could not unmarshal message")
			}
			resp := &cvmsgspb.VizierSSLCertResponse{}
			err = types.UnmarshalAny(pb.Msg, resp)
			if err != nil {
				t.Fatal("Could not unmarshal any message")
			}
			assert.Equal(t, "abcd", resp.Key)
			assert.Equal(t, "efgh", resp.Cert)
		case <-time.After(1 * time.Second):
			t.Fatal("Timeout")
		}
	})
}

func TestServer_UpdateOrInstallVizier(t *testing.T) {
	viper.Set("jwt_signing_key", "jwtkey")

	mustLoadTestData(db)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockDNSClient := mock_dnsmgrpb.NewMockDNSMgrServiceClient(ctrl)

	nc, cleanup := testingutils.MustStartTestNATS(t)
	defer cleanup()

	vizierID, _ := uuid.FromString("123e4567-e89b-12d3-a456-426655440001")

	updateResp := &cvmsgspb.UpdateOrInstallVizierResponse{
		UpdateStarted: true,
	}
	respAnyMsg, err := types.MarshalAny(updateResp)
	require.NoError(t, err)
	wrappedMsg := &cvmsgspb.V2CMessage{
		VizierID: vizierID.String(),
		Msg:      respAnyMsg,
	}
	updater := mock_controller.NewMockVzUpdater(ctrl)
	updater.
		EXPECT().
		UpdateOrInstallVizier(vizierID, "1.3.0", false).
		Return(wrappedMsg, nil)

	s := controller.New(db, "test", mockDNSClient, nc, updater)

	req := &cvmsgspb.UpdateOrInstallVizierRequest{
		VizierID: utils.ProtoFromUUIDStrOrNil(vizierID.String()),
		Version:  "1.3.0",
	}

	resp, err := s.UpdateOrInstallVizier(CreateTestContext(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)
}

func TestServer_MessageHandler(t *testing.T) {
	mustLoadTestData(db)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockDNSClient := mock_dnsmgrpb.NewMockDNSMgrServiceClient(ctrl)

	nc, cleanup := testingutils.MustStartTestNATS(t)
	defer cleanup()

	subCh := make(chan *nats.Msg, 1)
	sub, err := nc.ChanSubscribe("c2v.123e4567-e89b-12d3-a456-426655440001.sslResp", subCh)
	if err != nil {
		t.Fatal("Could not subscribe to NATS.")
	}
	defer func() {
		err = sub.Unsubscribe()
		require.NoError(t, err)
	}()

	_ = controller.New(db, "test", mockDNSClient, nc, nil)

	dnsMgrReq := &dnsmgrpb.GetSSLCertsRequest{
		ClusterID: utils.ProtoFromUUIDStrOrNil("123e4567-e89b-12d3-a456-426655440001"),
	}
	dnsMgrResp := &dnsmgrpb.GetSSLCertsResponse{
		Key:  "abcd",
		Cert: "efgh",
	}

	mockDNSClient.EXPECT().
		GetSSLCerts(gomock.Any(), dnsMgrReq).
		Return(dnsMgrResp, nil)

	// Publish v2c message over nats.
	nestedMsg := &cvmsgspb.VizierSSLCertRequest{
		VizierID: utils.ProtoFromUUIDStrOrNil("123e4567-e89b-12d3-a456-426655440001"),
	}
	nestedAny, err := types.MarshalAny(nestedMsg)
	if err != nil {
		t.Fatal("Could not marshal pb")
	}
	wrappedMsg := &cvmsgspb.V2CMessage{
		VizierID: "123e4567-e89b-12d3-a456-426655440001",
		Msg:      nestedAny,
	}

	b, err := wrappedMsg.Marshal()
	if err != nil {
		t.Fatal("Could not marshal message to bytes")
	}
	err = nc.Publish("v2c.1.123e4567-e89b-12d3-a456-426655440001.ssl", b)
	require.NoError(t, err)

	select {
	case m := <-subCh:
		pb := &cvmsgspb.C2VMessage{}
		err = proto.Unmarshal(m.Data, pb)
		if err != nil {
			t.Fatal("Could not unmarshal message")
		}
		resp := &cvmsgspb.VizierSSLCertResponse{}
		err = types.UnmarshalAny(pb.Msg, resp)
		if err != nil {
			t.Fatal("Could not unmarshal any message")
		}
		assert.Equal(t, "abcd", resp.Key)
		assert.Equal(t, "efgh", resp.Cert)
	case <-time.After(1 * time.Second):
		t.Fatal("Timeout")
	}
}

func TestServer_GetViziersByShard(t *testing.T) {
	mustLoadTestData(db)

	s := controller.New(db, "test", nil, nil, nil)

	tests := []struct {
		name string

		// Inputs:
		toShardID   string
		fromShardID string

		// Outputs:
		expectGRPCError error
		expectResponse  *vzmgrpb.GetViziersByShardResponse
	}{
		{
			name:            "Bad shardID",
			toShardID:       "gf",
			fromShardID:     "ff",
			expectGRPCError: status.Error(codes.InvalidArgument, "bad arg"),
		},
		{
			name:            "Bad shardID (valid hex, too large)",
			toShardID:       "fff",
			fromShardID:     "ff",
			expectGRPCError: status.Error(codes.InvalidArgument, "bad arg"),
		},
		{
			name:            "Bad shardID (valid hex, too small)",
			toShardID:       "f",
			fromShardID:     "ff",
			expectGRPCError: status.Error(codes.InvalidArgument, "bad arg"),
		},
		{
			name:            "Bad shardID (misordered)",
			toShardID:       "aa",
			fromShardID:     "ff",
			expectGRPCError: status.Error(codes.InvalidArgument, "bad arg"),
		},
		{
			name:            "Single vizier response",
			toShardID:       "00",
			fromShardID:     "00",
			expectGRPCError: nil,
			expectResponse: &vzmgrpb.GetViziersByShardResponse{
				Viziers: []*vzmgrpb.GetViziersByShardResponse_VizierInfo{
					{
						VizierID: utils.ProtoFromUUIDStrOrNil("123e4567-e89b-12d3-a456-426655440000"),
						OrgID:    utils.ProtoFromUUIDStrOrNil("223e4567-e89b-12d3-a456-426655440000"),
						K8sUID:   "k8sID",
					},
				},
			},
		},
		{
			name:            "Multi vizier response",
			toShardID:       "03",
			fromShardID:     "03",
			expectGRPCError: nil,
			expectResponse: &vzmgrpb.GetViziersByShardResponse{
				Viziers: []*vzmgrpb.GetViziersByShardResponse_VizierInfo{
					{
						VizierID: utils.ProtoFromUUIDStrOrNil("323e4567-e89b-12d3-a456-426655440003"),
						OrgID:    utils.ProtoFromUUIDStrOrNil("223e4567-e89b-12d3-a456-426655440001"),
						K8sUID:   "k8s6",
					},
					{
						VizierID: utils.ProtoFromUUIDStrOrNil("223e4567-e89b-12d3-a456-426655440003"),
						OrgID:    utils.ProtoFromUUIDStrOrNil("223e4567-e89b-12d3-a456-426655440001"),
						K8sUID:   "k8s5",
					},
				},
			},
		},
		{
			name:            "Multi range response",
			toShardID:       "03",
			fromShardID:     "00",
			expectGRPCError: nil,
			expectResponse: &vzmgrpb.GetViziersByShardResponse{
				Viziers: []*vzmgrpb.GetViziersByShardResponse_VizierInfo{
					{
						VizierID: utils.ProtoFromUUIDStrOrNil("123e4567-e89b-12d3-a456-426655440000"),
						OrgID:    utils.ProtoFromUUIDStrOrNil("223e4567-e89b-12d3-a456-426655440000"),
						K8sUID:   "k8sID",
					},
					{
						VizierID: utils.ProtoFromUUIDStrOrNil("123e4567-e89b-12d3-a456-426655440001"),
						OrgID:    utils.ProtoFromUUIDStrOrNil("223e4567-e89b-12d3-a456-426655440000"),
						K8sUID:   "cUID",
					},
					{
						VizierID: utils.ProtoFromUUIDStrOrNil("123e4567-e89b-12d3-a456-426655440002"),
						OrgID:    utils.ProtoFromUUIDStrOrNil("223e4567-e89b-12d3-a456-426655440000"),
						K8sUID:   "k8s2",
					},
					{
						VizierID: utils.ProtoFromUUIDStrOrNil("323e4567-e89b-12d3-a456-426655440003"),
						OrgID:    utils.ProtoFromUUIDStrOrNil("223e4567-e89b-12d3-a456-426655440001"),
						K8sUID:   "k8s6",
					},
					{
						VizierID: utils.ProtoFromUUIDStrOrNil("223e4567-e89b-12d3-a456-426655440003"),
						OrgID:    utils.ProtoFromUUIDStrOrNil("223e4567-e89b-12d3-a456-426655440001"),
						K8sUID:   "k8s5",
					},
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := &vzmgrpb.GetViziersByShardRequest{ToShardID: test.toShardID, FromShardID: test.fromShardID}
			resp, err := s.GetViziersByShard(context.Background(), req)

			if test.expectGRPCError != nil {
				assert.Equal(t, status.Code(test.expectGRPCError), status.Code(err))
			} else {
				assert.ElementsMatch(t, test.expectResponse.Viziers, resp.Viziers)
			}
		})
	}
}

func TestServer_ProvisionOrClaimVizier(t *testing.T) {
	mustLoadTestData(db)

	s := controller.New(db, "test", nil, nil, nil)
	// TODO(zasgar): We need to make user IDS make sense.
	userID := uuid.Must(uuid.NewV4())

	// This should select the first cluster with an empty UID that is disconnected.
	clusterID, err := s.ProvisionOrClaimVizier(context.Background(), uuid.FromStringOrNil(testAuthOrgID), userID, "my cluster", "", "1.1")
	require.NoError(t, err)
	// Should select the disconnected cluster.
	assert.Equal(t, testDisconnectedClusterEmptyUID, clusterID.String())
}

func TestServer_ProvisionOrClaimVizierWIthExistingUID(t *testing.T) {
	tests := []struct {
		name         string
		inputName    string
		expectedName string
	}{
		{
			name:         "same name",
			inputName:    "test_cluster_1234",
			expectedName: "test_cluster_1234",
		},
		{
			name:         "same name no prefix",
			inputName:    "test_cluster",
			expectedName: "test_cluster_1234",
		},
		{
			name:         "no input name",
			inputName:    "",
			expectedName: "test_cluster_1234",
		},
		{
			name:         "new name",
			inputName:    "new_name",
			expectedName: "new_name",
		},
		{
			name:         "name with spaces",
			inputName:    "test_cluster_1234        ",
			expectedName: "test_cluster_1234",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mustLoadTestData(db)

			s := controller.New(db, "test", nil, nil, nil)
			userID := uuid.Must(uuid.NewV4())

			// This should select the existing cluster with the same UID.
			clusterID, err := s.ProvisionOrClaimVizier(context.Background(), uuid.FromStringOrNil(testAuthOrgID), userID, "existing_cluster", test.inputName, "1.1")
			require.NoError(t, err)
			// Should select the disconnected cluster.
			assert.Equal(t, testExistingCluster, clusterID.String())

			// Check cluster name.
			var clusterInfo struct {
				ClusterName *string `db:"cluster_name"`
			}
			nameQuery := `SELECT cluster_name from vizier_cluster WHERE id=$1`
			err = db.Get(&clusterInfo, nameQuery, clusterID)
			require.NoError(t, err)
			assert.Equal(t, *clusterInfo.ClusterName, test.expectedName)
		})
	}
}

func TestServer_ProvisionOrClaimVizier_WithExistingActiveUID(t *testing.T) {
	mustLoadTestData(db)

	s := controller.New(db, "test", nil, nil, nil)
	userID := uuid.Must(uuid.NewV4())
	// This should select cause an error b/c we are trying to provision a cluster that is not disconnected.
	clusterID, err := s.ProvisionOrClaimVizier(context.Background(), uuid.FromStringOrNil(testAuthOrgID), userID, "my_other_cluster", "", "1.1")
	assert.NotNil(t, err)
	assert.Equal(t, vzerrors.ErrProvisionFailedVizierIsActive, err)
	assert.Equal(t, uuid.Nil, clusterID)
}

func TestServer_ProvisionOrClaimVizier_WithNewCluster(t *testing.T) {
	mustLoadTestData(db)

	s := controller.New(db, "test", nil, nil, nil)
	userID := uuid.Must(uuid.NewV4())
	// This should select cause an error b/c we are trying to provision a cluster that is not disconnected.
	clusterID, err := s.ProvisionOrClaimVizier(context.Background(), uuid.FromStringOrNil(testNonAuthOrgID), userID, "my_other_cluster", "", "1.1")
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, clusterID)
}

func TestServer_ProvisionOrClaimVizier_WithExistingName(t *testing.T) {
	mustLoadTestData(db)

	s := controller.New(db, "test", nil, nil, nil)
	userID := uuid.Must(uuid.NewV4())

	// This should select the existing cluster with the same UID.
	clusterID, err := s.ProvisionOrClaimVizier(context.Background(), uuid.FromStringOrNil(testAuthOrgID), userID, "some_cluster", "test_cluster_1234\n", "1.1")
	require.NoError(t, err)
	// Should select the disconnected cluster.
	assert.Equal(t, testDisconnectedClusterEmptyUID, clusterID.String())
	// Check cluster name.
	var clusterInfo struct {
		ClusterName    *string `db:"cluster_name"`
		ClusterVersion *string `db:"cluster_version"`
	}
	nameQuery := `SELECT cluster_name, cluster_version from vizier_cluster WHERE id=$1`
	err = db.Get(&clusterInfo, nameQuery, clusterID)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(*clusterInfo.ClusterName, "test_cluster_1234_"))
	assert.Equal(t, "1.1", *clusterInfo.ClusterVersion)
}
