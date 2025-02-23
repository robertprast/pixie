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

import { createBrowserHistory } from 'history';
import pixieAnalytics from 'app/utils/analytics';

const history = createBrowserHistory();

function showIntercom(path: string): boolean {
  return path === '/auth/login' || path === '/auth/signup';
}

function sendPageEvent(path: string) {
  pixieAnalytics.page(
    '', // category
    path, // name
    {}, // properties
    {
      integrations: {
        Intercom: { hideDefaultLauncher: !showIntercom(path) },
      },
    }, // options
  );
}

// Emit a page event for the first loaded page.
sendPageEvent(window.location.pathname);

history.listen((location) => {
  sendPageEvent(location.pathname);
});

export default history;
