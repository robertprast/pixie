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
	"testing"

	"github.com/gofrs/uuid"
	"github.com/gogo/protobuf/types"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"px.dev/pixie/src/api/proto/cloudpb"
	"px.dev/pixie/src/cloud/api/controller"
	"px.dev/pixie/src/cloud/api/controller/testutils"
	"px.dev/pixie/src/cloud/profile/profilepb"
	"px.dev/pixie/src/utils"
)

func TestServer_UpdateUser(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	_, mockClients, cleanup := testutils.CreateTestAPIEnv(t)
	defer cleanup()
	ctx := CreateTestContext()

	updateUserTest := []struct {
		name              string
		userID            string
		userOrg           string
		updatedProfilePic string
		updatedIsApproved bool
		shouldReject      bool
	}{
		{
			name:              "user can update their own profile picture",
			userID:            "6ba7b810-9dad-11d1-80b4-00c04fd430c9",
			userOrg:           "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
			updatedProfilePic: "new",
			updatedIsApproved: false,
			shouldReject:      false,
		},
		{
			name:              "admin can update another's profile picture",
			userID:            "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
			userOrg:           "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
			updatedProfilePic: "new",
			updatedIsApproved: false,
			shouldReject:      false,
		},
		{
			name:              "user cannot update their own isApproved",
			userID:            "6ba7b810-9dad-11d1-80b4-00c04fd430c9",
			userOrg:           "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
			updatedProfilePic: "new",
			updatedIsApproved: true,
			shouldReject:      true,
		},
		{
			name:              "user cannot update user from another org",
			userID:            "6ba7b810-9dad-11d1-80b4-00c04fd430c9",
			userOrg:           "7ba7b810-9dad-11d1-80b4-00c04fd430c8",
			updatedProfilePic: "new",
			updatedIsApproved: false,
			shouldReject:      true,
		},
		{
			name:              "user cannot update user from another org",
			userID:            "6ba7b810-9dad-11d1-80b4-00c04fd430c9",
			userOrg:           "7ba7b810-9dad-11d1-80b4-00c04fd430c8",
			updatedProfilePic: "something",
			updatedIsApproved: true,
			shouldReject:      true,
		},
		{
			name:              "user should approve other user in org",
			userID:            "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
			userOrg:           "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
			updatedProfilePic: "something",
			updatedIsApproved: true,
			shouldReject:      false,
		},
	}

	for _, tc := range updateUserTest {
		t.Run(tc.name, func(t *testing.T) {
			reqUserID := uuid.FromStringOrNil(tc.userID)
			reqOrgID := uuid.FromStringOrNil(tc.userOrg)

			profilePicture := "something"
			updatedUserInfo := &profilepb.UserInfo{
				ID:             utils.ProtoFromUUID(reqUserID),
				FirstName:      "first",
				LastName:       "last",
				ProfilePicture: profilePicture,
				IsApproved:     false,
				OrgID:          utils.ProtoFromUUID(reqOrgID),
			}

			req := &cloudpb.UpdateUserRequest{
				ID: utils.ProtoFromUUID(reqUserID),
			}

			mockUpdateReq := &profilepb.UpdateUserRequest{
				ID: utils.ProtoFromUUID(reqUserID),
			}

			if tc.updatedProfilePic != profilePicture {
				req.DisplayPicture = &types.StringValue{Value: tc.updatedProfilePic}
				mockUpdateReq.DisplayPicture = &types.StringValue{Value: tc.updatedProfilePic}
				updatedUserInfo.ProfilePicture = tc.updatedProfilePic
			}

			if tc.updatedIsApproved != updatedUserInfo.IsApproved {
				req.IsApproved = &types.BoolValue{Value: tc.updatedIsApproved}
				mockUpdateReq.IsApproved = &types.BoolValue{Value: tc.updatedIsApproved}
				updatedUserInfo.IsApproved = tc.updatedIsApproved
			}

			mockClients.MockProfile.EXPECT().GetUser(gomock.Any(), utils.ProtoFromUUID(reqUserID)).
				Return(&profilepb.UserInfo{
					ID:    utils.ProtoFromUUID(reqUserID),
					OrgID: utils.ProtoFromUUID(reqOrgID),
				}, nil)

			if !tc.shouldReject {
				mockClients.MockProfile.EXPECT().UpdateUser(gomock.Any(), mockUpdateReq).
					Return(updatedUserInfo, nil)
			}

			userServer := &controller.UserServiceServer{mockClients.MockProfile}
			resp, err := userServer.UpdateUser(ctx, req)

			if !tc.shouldReject {
				require.NoError(t, err)
				assert.Equal(t, resp.ID, utils.ProtoFromUUID(reqUserID))
				assert.Equal(t, resp.ProfilePicture, tc.updatedProfilePic)
				assert.Equal(t, resp.IsApproved, tc.updatedIsApproved)
			} else {
				assert.NotNil(t, err)
			}
		})
	}
}
