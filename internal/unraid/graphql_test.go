package unraid

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
)

func TestGraphQLClient_Do_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "test-key", r.Header.Get("x-api-key"))
		_, _ = w.Write([]byte(`{"data":{"vars":{"version":"7.3.2"}}}`))
	}))
	defer srv.Close()

	c := newGraphQLClient(srv.URL, "test-key")
	var out struct {
		Vars struct {
			Version string `json:"version"`
		} `json:"vars"`
	}
	err := c.do(context.Background(), "{ vars { version } }", &out)
	require.NoError(t, err)
	assert.Equal(t, "7.3.2", out.Vars.Version)
}

func TestGraphQLClient_Do_HTTPUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := newGraphQLClient(srv.URL, "bad-key")
	err := c.do(context.Background(), "{ vars { version } }", nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrUnauthenticated)
}

func TestGraphQLClient_Do_GraphQLErrorPayload(t *testing.T) {
	// Exact shape confirmed live against the reference host for an
	// unauthenticated real-field read (docs/research/unraid-state-sources.md
	// "GraphQL reachability" section + graphql.go's isAuthError comment).
	tests := []struct {
		name string
		body string
	}{
		{
			name: "structured UNAUTHENTICATED code",
			body: `{"errors":[{"message":"Invalid CSRF token","extensions":{"code":"UNAUTHENTICATED"}}],"data":null}`,
		},
		{
			name: "session message with no code",
			body: `{"errors":[{"message":"No user session found"}],"data":null}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := newGraphQLClient(srv.URL, "")
			err := c.do(context.Background(), "{ array { state } }", nil)
			require.Error(t, err)
			assert.ErrorIs(t, err, core.ErrUnauthenticated)
		})
	}
}

func TestGraphQLClient_Do_NonAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{"message":"field \"bogus\" does not exist on type Query"}],"data":null}`))
	}))
	defer srv.Close()

	c := newGraphQLClient(srv.URL, "")
	err := c.do(context.Background(), "{ bogus }", nil)
	require.Error(t, err)
	assert.NotErrorIs(t, err, core.ErrUnauthenticated)
}

func TestBigInt_UnmarshalJSON(t *testing.T) {
	// unraid-api's BigInt scalar has changed wire representation across
	// releases (docs/research/unraid-state-sources.md "Version stability");
	// accept both a bare number and a quoted string.
	for _, raw := range []string{`42`, `"42"`} {
		var b bigInt
		require.NoError(t, json.Unmarshal([]byte(raw), &b))
		assert.Equal(t, bigInt(42), b)
	}
	var zero bigInt
	require.NoError(t, json.Unmarshal([]byte(`null`), &zero))
	assert.Equal(t, bigInt(0), zero)
}
