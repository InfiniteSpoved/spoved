// Copyright 2020 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	activities_model "forgejo.org/models/activities"
	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/eventsource"
	"forgejo.org/modules/setting"
	api "forgejo.org/modules/structs"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventSourceManagerRun(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	manager := eventsource.GetManager()

	eventChan := manager.Register(2)
	defer func() {
		manager.Unregister(2, eventChan)
		// ensure the eventChan is closed
		for {
			_, ok := <-eventChan
			if !ok {
				break
			}
		}
	}()
	expectNotificationCountEvent := func(count int64) func() bool {
		return func() bool {
			select {
			case event, ok := <-eventChan:
				if !ok {
					return false
				}
				data, ok := event.Data.(activities_model.UserIDCount)
				if !ok {
					return false
				}
				return event.Name == "notification-count" && data.Count == count
			default:
				return false
			}
		}
	}

	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	repo1 := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	thread5 := unittest.AssertExistsAndLoadBean(t, &activities_model.Notification{ID: 5})
	require.NoError(t, thread5.LoadAttributes(db.DefaultContext))
	session := loginUser(t, user2.Name)
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteNotification, auth_model.AccessTokenScopeWriteRepository)

	var apiNL []api.NotificationThread

	// -- mark notifications as read --
	req := NewRequest(t, "GET", "/api/v1/notifications?status-types=unread").
		AddTokenAuth(token)
	resp := session.MakeRequest(t, req, http.StatusOK)

	DecodeJSON(t, resp, &apiNL)
	assert.Len(t, apiNL, 2)

	lastReadAt := "2000-01-01T00%3A50%3A01%2B00%3A00" // 946687801 <- only Notification 4 is in this filter ...
	req = NewRequest(t, "PUT", fmt.Sprintf("/api/v1/repos/%s/%s/notifications?last_read_at=%s", user2.Name, repo1.Name, lastReadAt)).
		AddTokenAuth(token)
	session.MakeRequest(t, req, http.StatusResetContent)

	req = NewRequest(t, "GET", "/api/v1/notifications?status-types=unread").
		AddTokenAuth(token)
	resp = session.MakeRequest(t, req, http.StatusOK)
	DecodeJSON(t, resp, &apiNL)
	assert.Len(t, apiNL, 1)

	assert.Eventually(t, expectNotificationCountEvent(1), 30*time.Second, 1*time.Second)
}

// The event stream must flush each event, with and without the Accept header (the session package passes the
// writer through only with it).
func TestEventSourceFlushes(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		cookie := loginUser(t, "user2").GetCookie(setting.SessionConfig.CookieName)
		require.NotNil(t, cookie)
		for _, accept := range []string{"text/event-stream", ""} {
			t.Run("accept "+accept, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				req, err := http.NewRequestWithContext(ctx, "GET", u.String()+"user/events", nil)
				require.NoError(t, err)
				req.AddCookie(cookie)
				if accept != "" {
					req.Header.Set("Accept", accept)
				}
				resp, err := http.DefaultClient.Do(req)
				require.NoError(t, err)
				defer resp.Body.Close()
				require.Equal(t, http.StatusOK, resp.StatusCode)
				go func() { // dropped until the handler has registered, so send until the test is done
					for ctx.Err() == nil {
						eventsource.GetManager().SendMessage(2, &eventsource.Event{Name: "flush-test", Data: "x"})
						time.Sleep(100 * time.Millisecond)
					}
				}()
				scanner := bufio.NewScanner(resp.Body)
				for scanner.Scan() {
					if scanner.Text() == "event: flush-test" {
						return
					}
				}
				t.Fatalf("no event before the timeout: %v", scanner.Err())
			})
		}
	})
}
