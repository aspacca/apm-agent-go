// Licensed to Elasticsearch B.V. under one or more contributor
// license agreements. See the NOTICE file distributed with
// this work for additional information regarding copyright
// ownership. Elasticsearch B.V. licenses this file to you under
// the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package apmgoredis_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/go-redis/redis"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.elastic.co/apm/apmtest"
	"go.elastic.co/apm/module/apmgoredis"
)

func TestMain(m *testing.M) {
	ret := m.Run()

	clusterReset()

	os.Exit(ret)
}

func TestRequestContext(t *testing.T) {
	testCases := getTestCases(t)

	for _, testCase := range testCases {
		client := testCase.client
		if client == nil {
			t.Errorf("cannot create client")
		}

		defer client.Close()
		cleanRedis(t, client, testCase.isCluster)

		_, spans, _ := apmtest.WithTransaction(func(ctx context.Context) {
			for i := 0; i < 2; i++ {
				wrappedClient := apmgoredis.Wrap(client).WithContext(ctx)

				cmd := wrappedClient.Get("content")
				if cmd.Err() == nil {
					return
				}

				value := []byte("Lorem ipsum dolor sit amet")
				if cmd := wrappedClient.Set("content", value, 0); cmd.Err() != nil {
					require.NoError(t, cmd.Err())
				}
			}
		})

		require.Len(t, spans, 3)

		assert.Equal(t, "GET", spans[0].Name)
		assert.Equal(t, "SET", spans[1].Name)
		assert.Equal(t, "GET", spans[2].Name)
	}
}

func TestPipelined(t *testing.T) {
	testCases := getTestCases(t)

	for _, testCase := range testCases {
		client := testCase.client
		if client == nil {
			t.Errorf("cannot create client")
		}

		defer client.Close()
		cleanRedis(t, client, testCase.isCluster)

		_, spans, _ := apmtest.WithTransaction(func(ctx context.Context) {
			wrappedClient := apmgoredis.Wrap(client).WithContext(ctx)

			_, err := wrappedClient.Pipelined(func(pipe redis.Pipeliner) error {
				err := pipe.Set("foo", "bar", 0).Err()
				require.NoError(t, err)

				err = pipe.Get("foo").Err()
				require.NoError(t, err)

				err = pipe.FlushDB().Err()
				require.NoError(t, err)

				return err
			})

			require.NoError(t, err)
		})

		assert.Len(t, spans, 1)
		assert.Equal(t, "(pipeline) SET GET FLUSHDB", spans[0].Name)
	}
}

func TestPipeline(t *testing.T) {
	testCases := getTestCases(t)

	for _, testCase := range testCases {
		client := testCase.client
		if client == nil {
			t.Errorf("cannot create client")
		}

		defer client.Close()
		cleanRedis(t, client, testCase.isCluster)

		_, spans, _ := apmtest.WithTransaction(func(ctx context.Context) {
			wrappedClient := apmgoredis.Wrap(client).WithContext(ctx)

			pipe := wrappedClient.Pipeline()

			err := pipe.Set("foo", "bar", 0).Err()
			require.NoError(t, err)

			err = pipe.Get("foo").Err()
			require.NoError(t, err)

			err = pipe.FlushDB().Err()
			require.NoError(t, err)

			_, err = pipe.Exec()
			require.NoError(t, err)
		})

		require.Len(t, spans, 1)
		assert.Equal(t, "(pipeline) SET GET FLUSHDB", spans[0].Name)
	}
}

func TestPipelinedTransaction(t *testing.T) {
	testCases := getTestCases(t)

	for _, testCase := range testCases {
		client := testCase.client
		if client == nil {
			t.Errorf("cannot create client")
		}

		defer client.Close()
		cleanRedis(t, client, testCase.isCluster)

		_, spans, _ := apmtest.WithTransaction(func(ctx context.Context) {
			wrappedClient := apmgoredis.Wrap(client).WithContext(ctx)

			var incr1 *redis.IntCmd
			var incr2 *redis.IntCmd
			var incr3 *redis.IntCmd
			_, err := wrappedClient.TxPipelined(func(pipe redis.Pipeliner) error {
				incr1 = pipe.Incr("foo")
				assert.NoError(t, incr1.Err())

				incr2 = pipe.Incr("bar")
				assert.NoError(t, incr2.Err())

				incr3 = pipe.Incr("bar")
				assert.NoError(t, incr3.Err())

				return nil
			})

			assert.Equal(t, int64(1), incr1.Val())
			assert.Equal(t, int64(1), incr2.Val())
			assert.Equal(t, int64(2), incr3.Val())

			assert.NoError(t, err)
		})

		switch testCase.isTxWrapped {
		case true:
			assert.Len(t, spans, 1)
			assert.Equal(t, "(pipeline) INCR INCR INCR", spans[0].Name)
		case false:
			assert.Len(t, spans, 0)
		}
	}
}

func TestPipelineTransaction(t *testing.T) {
	testCases := getTestCases(t)

	for _, testCase := range testCases {
		client := testCase.client
		if client == nil {
			t.Errorf("cannot create client")
		}

		defer client.Close()
		cleanRedis(t, client, testCase.isCluster)

		_, spans, _ := apmtest.WithTransaction(func(ctx context.Context) {
			wrappedClient := apmgoredis.Wrap(client).WithContext(ctx)

			pipe := wrappedClient.TxPipeline()

			incr1 := pipe.Incr("foo")
			assert.NoError(t, incr1.Err())

			incr2 := pipe.Incr("bar")
			assert.NoError(t, incr2.Err())

			incr3 := pipe.Incr("bar")
			assert.NoError(t, incr3.Err())

			_, err := pipe.Exec()
			require.NoError(t, err)

			assert.Equal(t, int64(1), incr1.Val())
			assert.Equal(t, int64(1), incr2.Val())
			assert.Equal(t, int64(2), incr3.Val())
		})

		switch testCase.isTxWrapped {
		case true:
			assert.Len(t, spans, 1)
			assert.Equal(t, "(pipeline) INCR INCR INCR", spans[0].Name)
		case false:
			assert.Len(t, spans, 0)
		}
	}
}

func redisClient(t *testing.T) *redis.Client {
	redisURL := os.Getenv("GOREDIS_URL")
	if redisURL == "" {
		t.Skipf("GOREDIS_URL not specified")
	}

	var err error
	redisURL, err = lookupIPFromHostPort(redisURL)
	if err != nil {
		return nil
	}

	closeConn := true
	client := redis.NewClient(&redis.Options{
		Addr: redisURL,
	})

	defer func() {
		if closeConn {
			client.Close()
		}
	}()

	closeConn = false
	return client
}

func redisClusterClient(t *testing.T) *redis.ClusterClient {
	redisURLs := strings.Split(os.Getenv("GOREDIS_CLUSTER_URLS"), " ")
	if len(redisURLs) == 0 {
		if t != nil {
			t.Skipf("GOREDIS_CLUSTER_URLS not specified")
		}
	}

	for i, redisURL := range redisURLs {
		var err error
		redisURLs[i], err = lookupIPFromHostPort(redisURL)
		if err != nil {
			return nil
		}
	}

	closeConn := true
	client := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: redisURLs,
	})

	defer func() {
		if closeConn {
			client.Close()
		}
	}()

	closeConn = false
	return client
}

func clusterReset() {
	client := redisClusterClient(nil)

	if client == nil {
		return
	}

	client.ForEachMaster(func(master *redis.Client) error {
		master.FlushDB().Err()
		master.ClusterResetSoft().Err()

		return nil
	})
}

func cleanRedis(t *testing.T, client redis.UniversalClient, isCluster bool) {
	switch isCluster {
	case false:
		st := client.FlushDB()
		require.NoError(t, st.Err())
	case true:
		var err error
		switch client.(type) {
		case *redis.ClusterClient:
			err = client.(*redis.ClusterClient).ForEachMaster(func(master *redis.Client) error {
				return master.FlushDB().Err()
			})
		case apmgoredis.Client:
			err = client.(apmgoredis.Client).Cluster().ForEachMaster(func(master *redis.Client) error {
				return master.FlushDB().Err()
			})
		}

		require.NoError(t, err)
	}
}

func lookupIPFromHostPort(hostPort string) (string, error) {
	data := strings.Split(hostPort, ":")
	ips, err := net.LookupIP(data[0])

	if len(ips) == 0 {
		return "", errors.New("cannot lookup ip")
	}

	return fmt.Sprintf("%s:%s", ips[0], data[1]), err
}

func getTestCases(t *testing.T) []struct {
	isCluster   bool
	isTxWrapped bool
	client      redis.UniversalClient
} {
	return []struct {
		isCluster   bool
		isTxWrapped bool
		client      redis.UniversalClient
	}{
		{
			false,
			true,
			redisClient(t),
		},
		{
			false,
			true,
			apmgoredis.Wrap(redisClient(t)),
		},
		{
			false,
			true,
			apmgoredis.Wrap(redisClient(t)).WithContext(context.Background()),
		},
		{
			true,
			false,
			redisClusterClient(t),
		},
		{
			true,
			false,
			apmgoredis.Wrap(redisClusterClient(t)),
		},
		{
			true,
			false,
			apmgoredis.Wrap(redisClusterClient(t)).WithContext(context.Background()),
		},
	}
}
