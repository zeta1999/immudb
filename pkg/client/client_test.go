/*
Copyright 2019-2020 vChain, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package client

import (
	"context"
	"io/ioutil"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/codenotary/immudb/pkg/client/cache"
	"github.com/codenotary/immudb/pkg/client/timestamp"

	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/codenotary/immudb/pkg/auth"
	"github.com/codenotary/immudb/pkg/logger"
	"github.com/codenotary/immudb/pkg/server"
	"github.com/codenotary/immudb/pkg/store"
	"github.com/stretchr/testify/require"

	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

const bufSize = 1024 * 1024

var lis *bufconn.Listener

var immuServer *server.ImmuServer
var client ImmuClient

const BkpFileName = "client_test.dump.bkp"
const ExpectedBkpFileName = "./../../test/client_test.expected.bkp"

var testData = struct {
	keys    [][]byte
	values  [][]byte
	refKeys [][]byte
	set     []byte
	scores  []float64
}{
	keys:    [][]byte{[]byte("key1"), []byte("key2"), []byte("key3")},
	values:  [][]byte{[]byte("value1"), []byte("value2"), []byte("value3")},
	refKeys: [][]byte{[]byte("refKey1"), []byte("refKey2"), []byte("refKey3")},
	set:     []byte("set1"),
	scores:  []float64{1.0, 2.0, 3.0},
}

var slog = logger.NewSimpleLoggerWithLevel("client_test", os.Stderr, logger.LogDebug)

var plainPass string

func newServer() *server.ImmuServer {
	is := server.DefaultServer()
	is = is.WithOptions(is.Options.WithAuth(true))
	auth.AuthEnabled = is.Options.Auth
	var err error
	sysDbDir := filepath.Join(is.Options.Dir, is.Options.SysDbName)
	if err = os.MkdirAll(sysDbDir, os.ModePerm); err != nil {
		log.Fatal(err)
	}
	is.SysStore, err = store.Open(store.DefaultOptions(sysDbDir, slog))
	if err != nil {
		log.Fatal(err)
	}
	dbDir := filepath.Join(is.Options.Dir, is.Options.DbName)
	if err = os.MkdirAll(dbDir, os.ModePerm); err != nil {
		log.Fatal(err)
	}
	is.Store, err = store.Open(store.DefaultOptions(dbDir, slog))
	if err != nil {
		log.Fatal(err)
	}

	_, plainPass, err = is.CreateAdminUser(context.Background())
	if err != nil {
		log.Fatal(err)
	}

	lis = bufconn.Listen(bufSize)
	s := grpc.NewServer(
		grpc.UnaryInterceptor(auth.ServerUnaryInterceptor),
		grpc.StreamInterceptor(auth.ServerStreamInterceptor),
	)
	schema.RegisterImmuServiceServer(s, is)
	go func() {
		if err := s.Serve(lis); err != nil {
			log.Fatal(err)
		}
	}()
	return is
}

func newClient(withToken bool, token string) ImmuClient {
	dialOptions := []grpc.DialOption{
		grpc.WithContextDialer(bufDialer), grpc.WithInsecure(),
	}
	if withToken {
		dialOptions = append(
			dialOptions,
			grpc.WithUnaryInterceptor(auth.ClientUnaryInterceptor(token)),
			grpc.WithStreamInterceptor(auth.ClientStreamInterceptor(token)),
		)
	}

	immuclient := DefaultClient().WithOptions(DefaultOptions().WithAuth(withToken).WithDialOptions(&dialOptions))
	clientConn, _ := immuclient.Connect(context.TODO())
	immuclient.WithClientConn(clientConn)
	serviceClient := schema.NewImmuServiceClient(clientConn)
	immuclient.WithServiceClient(serviceClient)
	rootService := NewRootService(serviceClient, cache.NewFileCache("."), logger.NewSimpleLogger("test", os.Stdout))
	immuclient.WithRootService(rootService)

	return immuclient
}

func login() string {
	c := newClient(false, "")
	ctx := context.Background()
	r, err := c.Login(ctx, []byte(auth.AdminUsername), []byte(plainPass))
	if err != nil {
		log.Fatal(err)
	}
	return string(r.GetToken())
}

type ntpMock struct {
	t time.Time
}

func NewNtpMock() (timestamp.TsGenerator, error) {
	i, err := strconv.ParseInt("1405544146", 10, 64)
	if err != nil {
		panic(err)
	}
	tm := time.Unix(i, 0)
	return &ntpMock{tm}, nil
}

func (n *ntpMock) Now() time.Time {
	return n.t
}

func init() {
	cleanup()
	cleanupDump()
	immuServer = newServer()
	nm, _ := NewNtpMock()
	tss := NewTimestampService(nm)
	token := login()
	client = newClient(true, token).WithTimestampService(tss)
}

func bufDialer(ctx context.Context, address string) (net.Conn, error) {
	return lis.Dial()
}

func cleanup() {
	// delete files and folders created by tests
	if err := os.Remove(".root-"); err != nil {
		log.Println(err)
	}
	dbDir := filepath.Join(server.DefaultOptions().Dir, server.DefaultOptions().DbName)
	if err := os.RemoveAll(dbDir); err != nil {
		log.Println(err)
	}
	sysDbDir := filepath.Join(server.DefaultOptions().Dir, server.DefaultOptions().SysDbName)
	if err := os.RemoveAll(sysDbDir); err != nil {
		log.Println(err)
	}
}
func cleanupDump() {
	if err := os.Remove(BkpFileName); err != nil {
		log.Println(err)
	}
}

func testSafeSetAndSafeGet(ctx context.Context, t *testing.T, key []byte, value []byte) {
	_, err2 := client.SafeSet(ctx, key, value)
	require.NoError(t, err2)
	vi, err := client.SafeGet(ctx, key)

	require.NoError(t, err)
	require.NotNil(t, vi)
	require.Equal(t, key, vi.Key)
	require.Equal(t, value, vi.Value)
	require.Equal(t, uint64(1405544146), vi.Time)
	require.True(t, vi.Verified)
}

func testSafeReference(ctx context.Context, t *testing.T, referenceKey []byte, key []byte, value []byte) {
	_, err2 := client.SafeReference(ctx, referenceKey, key)
	require.NoError(t, err2)
	vi, err := client.SafeGet(ctx, referenceKey)
	require.NoError(t, err)
	require.NotNil(t, vi)
	require.Equal(t, key, vi.Key)
	require.Equal(t, value, vi.Value)
	require.Equal(t, uint64(1405544146), vi.Time)
	require.True(t, vi.Verified)
}

func testSafeZAdd(ctx context.Context, t *testing.T, set []byte, scores []float64, keys [][]byte, values [][]byte) {
	for i := 0; i < len(scores); i++ {
		_, err := client.SafeZAdd(ctx, set, scores[i], keys[i])
		require.NoError(t, err)
	}
	itemList, err := client.ZScan(ctx, set)
	require.NoError(t, err)
	require.NotNil(t, itemList)
	require.Len(t, itemList.Items, len(keys))

	for i := 0; i < len(keys); i++ {
		require.Equal(t, keys[i], itemList.Items[i].Key)
		require.Equal(t, values[i], itemList.Items[i].Value.Payload)
		require.Equal(t, uint64(1405544146), itemList.Items[i].Value.Timestamp)
	}
}

func testGetByRawIndexOnSafeZAdd(ctx context.Context, t *testing.T, set []byte, scores []float64, keys [][]byte, values [][]byte) {

	vi1, err1 := client.RawSafeSet(ctx, []byte("key-n1"), []byte("val-n1"))
	require.True(t, vi1.Verified)
	require.NoError(t, err1)
	vi2, err2 := client.SafeZAdd(ctx, []byte("set-n1"), 98.5, []byte("key-n1"))
	require.True(t, vi2.Verified)
	require.NoError(t, err2)

	item1, err3 := client.RawBySafeIndex(ctx, 9)
	require.True(t, item1.Verified)
	require.Equal(t, []byte("val-n1"), item1.Value)
	require.NoError(t, err3)
	item2, err2 := client.RawBySafeIndex(ctx, 1)
	require.True(t, item2.Verified)
	require.NoError(t, err2)
	item3, err3 := client.RawBySafeIndex(ctx, 2)
	require.True(t, item3.Verified)
	require.NoError(t, err3)
}

func testGetByRawIndexOnZAdd(ctx context.Context, t *testing.T, set []byte, scores []float64, keys [][]byte, values [][]byte) {
	vi1, err1 := client.RawSafeSet(ctx, []byte("key-n11"), []byte("val-n11"))
	require.True(t, vi1.Verified)
	require.NoError(t, err1)
	index, err2 := client.ZAdd(ctx, []byte("set-n11"), 98.5, []byte("key-n11"))
	require.NoError(t, err2)

	item1, err3 := client.RawBySafeIndex(ctx, index.Index)
	require.True(t, item1.Verified)
	require.Equal(t, []byte("key-n11"), item1.Value)
	require.NoError(t, err3)
}

func testDump(ctx context.Context, t *testing.T) {
	bkpFile, err := os.Create(BkpFileName)
	require.NoError(t, err)
	n, err := client.Dump(ctx, bkpFile)

	require.NoError(t, err)
	require.Equal(t, int64(38), n)

	bkpBytesActual, err := ioutil.ReadFile(BkpFileName)
	require.NoError(t, err)
	require.NotEmpty(t, bkpBytesActual)
	bkpBytesExpected, err := ioutil.ReadFile(ExpectedBkpFileName)
	require.NoError(t, err)
	require.NotEmpty(t, bkpBytesExpected)
	require.Equal(t, bkpBytesExpected, bkpBytesActual)
}

func TestImmuClient(t *testing.T) {
	cleanup()
	cleanupDump()
	defer cleanup()
	defer cleanupDump()

	ctx := context.Background()

	testSafeSetAndSafeGet(ctx, t, testData.keys[0], testData.values[0])
	testSafeSetAndSafeGet(ctx, t, testData.keys[1], testData.values[1])
	testSafeSetAndSafeGet(ctx, t, testData.keys[2], testData.values[2])

	testSafeReference(ctx, t, testData.refKeys[0], testData.keys[0], testData.values[0])
	testSafeReference(ctx, t, testData.refKeys[1], testData.keys[1], testData.values[1])
	testSafeReference(ctx, t, testData.refKeys[2], testData.keys[2], testData.values[2])

	testSafeZAdd(ctx, t, testData.set, testData.scores, testData.keys, testData.values)
	testGetByRawIndexOnSafeZAdd(ctx, t, testData.set, testData.scores, testData.keys, testData.values)
	testGetByRawIndexOnZAdd(ctx, t, testData.set, testData.scores, testData.keys, testData.values)
	testDump(ctx, t)

}

// todo(joe-dz): Enable restore when the feature is required again.
//func TestRestore(t *testing.T) {
//	cleanup()
//	defer cleanup()
//
//	ctx := context.Background()
//
//	// this only succeeds if only this test function is run, otherwise the key may
//	// be present from other test function that run before this:
//	// r1, err := client.Connected(ctx, func() (interface{}, error) {
//	// 	return client.SafeGet(ctx, testData.keys[1])
//	// })
//	// require.Error(t, err)
//	// require.Nil(t, r1)
//
//	bkpFileForRead, err := os.Open(ExpectedBkpFileName)
//	require.NoError(t, err)
//	r2, err := client.Connected(ctx, func() (interface{}, error) {
//		return client.Restore(ctx, bkpFileForRead, 20)
//	})
//	require.NoError(t, err)
//	n2 := r2.(int64)
//	require.Equal(t, int64(26), n2)
//
//	r3, err := client.Connected(ctx, func() (interface{}, error) {
//		return client.SafeGet(ctx, testData.keys[1])
//	})
//	require.NoError(t, err)
//	require.NotNil(t, r3)
//	vi := r3.(*VerifiedItem)
//	require.Equal(t, testData.keys[1], vi.Key)
//	require.Equal(t, testData.values[1], vi.Value)
//	require.True(t, vi.Verified)
//
//	r4, err := client.Connected(ctx, func() (interface{}, error) {
//		return client.SafeGet(ctx, testData.refKeys[2])
//	})
//	require.NoError(t, err)
//	require.NotNil(t, r4)
//	viFromRef := r4.(*VerifiedItem)
//	require.Equal(t, testData.keys[2], viFromRef.Key)
//	require.Equal(t, testData.values[2], viFromRef.Value)
//	require.True(t, viFromRef.Verified)
//
//	r5, err := client.Connected(ctx, func() (interface{}, error) {
//		return client.ZScan(ctx, testData.set)
//	})
//	require.NoError(t, err)
//	require.NotNil(t, r5)
//	itemList := r5.(*schema.StructuredItemList)
//	require.Len(t, itemList.Items, len(testData.keys))
//
//	for i := 0; i < len(testData.keys); i++ {
//		require.Equal(t, testData.keys[i], itemList.Items[i].Key)
//		require.Equal(t, testData.values[i], itemList.Items[i].Value.Payload)
//	}
//}
