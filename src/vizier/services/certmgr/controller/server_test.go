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
	"testing"

	"github.com/gofrs/uuid"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"px.dev/pixie/src/vizier/services/certmgr/certmgrpb"
	"px.dev/pixie/src/vizier/services/certmgr/controller"
	mock_controller "px.dev/pixie/src/vizier/services/certmgr/controller/mock"
)

func TestServer_UpdateCerts(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockK8s := mock_controller.NewMockK8sAPI(ctrl)

	s := controller.NewServer(nil, uuid.Must(uuid.NewV4()), nil, mockK8s)

	req := &certmgrpb.UpdateCertsRequest{
		Key:  "abc",
		Cert: "def",
	}

	mockK8s.EXPECT().
		CreateTLSSecret("proxy-tls-certs", "abc", "def").
		Return(nil)

	mockK8s.EXPECT().
		GetPodNamesForService("vizier-proxy-service").
		Return([]string{"vizier-proxy-service-pod", "test"}, nil)

	mockK8s.EXPECT().
		DeletePod("vizier-proxy-service-pod").
		Return(nil)

	mockK8s.EXPECT().
		DeletePod("test").
		Return(nil)

	resp, err := s.UpdateCerts(context.Background(), req)
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, true, resp.OK)
}

func TestServer_UpdateCerts_SecretsFailed(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockK8s := mock_controller.NewMockK8sAPI(ctrl)

	s := controller.NewServer(nil, uuid.Must(uuid.NewV4()), nil, mockK8s)

	req := &certmgrpb.UpdateCertsRequest{
		Key:  "abc",
		Cert: "def",
	}

	mockK8s.EXPECT().
		CreateTLSSecret("proxy-tls-certs", "abc", "def").
		Return(errors.New("Could not create secret"))

	resp, err := s.UpdateCerts(context.Background(), req)
	assert.Nil(t, resp)
	assert.NotNil(t, err)
}

func TestServer_UpdateCerts_NoPods(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockK8s := mock_controller.NewMockK8sAPI(ctrl)

	s := controller.NewServer(nil, uuid.Must(uuid.NewV4()), nil, mockK8s)

	req := &certmgrpb.UpdateCertsRequest{
		Key:  "abc",
		Cert: "def",
	}

	mockK8s.EXPECT().
		CreateTLSSecret("proxy-tls-certs", "abc", "def").
		Return(nil)

	mockK8s.EXPECT().
		GetPodNamesForService("vizier-proxy-service").
		Return([]string{}, nil)

	resp, err := s.UpdateCerts(context.Background(), req)
	assert.Nil(t, resp)
	assert.NotNil(t, err)
}

func TestServer_UpdateCerts_FailedPodDeletion(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockK8s := mock_controller.NewMockK8sAPI(ctrl)

	s := controller.NewServer(nil, uuid.Must(uuid.NewV4()), nil, mockK8s)

	req := &certmgrpb.UpdateCertsRequest{
		Key:  "abc",
		Cert: "def",
	}

	mockK8s.EXPECT().
		CreateTLSSecret("proxy-tls-certs", "abc", "def").
		Return(nil)

	mockK8s.EXPECT().
		GetPodNamesForService("vizier-proxy-service").
		Return([]string{"vizier-proxy-service-pod"}, nil)

	mockK8s.EXPECT().
		DeletePod("vizier-proxy-service-pod").
		Return(errors.New("Could not delete pod"))

	resp, err := s.UpdateCerts(context.Background(), req)
	assert.Nil(t, resp)
	assert.NotNil(t, err)
}
