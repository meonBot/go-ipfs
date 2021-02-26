package migrations

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"strings"
	"testing"
)

func createTestServer() *httptest.Server {
	reqHandler := func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if strings.Contains(r.URL.Path, "not-here") {
			http.NotFound(w, r)
		} else if strings.HasSuffix(r.URL.Path, "versions") {
			fmt.Fprint(w, "v1.0.0\nv1.1.0\nv1.1.2\nv2.0.0-rc1\n2.0.0\nv2.0.1\n")
		} else if strings.HasSuffix(r.URL.Path, ".tar.gz") {
			createFakeArchive(r.URL.Path, false, w)
		} else if strings.HasSuffix(r.URL.Path, "zip") {
			createFakeArchive(r.URL.Path, true, w)
		} else {
			http.NotFound(w, r)
		}
	}
	return httptest.NewServer(http.HandlerFunc(reqHandler))
}

func createFakeArchive(name string, archZip bool, w io.Writer) {
	fileName := strings.Split(path.Base(name), "_")[0]
	root := path.Base(path.Dir(path.Dir(name)))

	// Simulate fetching go-ipfs, which has "ipfs" as the name in the archive.
	if fileName == "go-ipfs" {
		fileName = "ipfs"
	}

	var err error
	if archZip {
		err = writeZip(root, fileName, "FAKE DATA", w)
	} else {
		err = writeTarGzip(root, fileName, "FAKE DATA", w)
	}
	if err != nil {
		panic(err)
	}
}

func TestSetDistPath(t *testing.T) {
	f1 := NewHttpFetcher()
	f2 := NewHttpFetcher()
	mf := NewMultiFetcher(f1, f2)

	os.Unsetenv(envIpfsDistPath)
	mf.SetDistPath(GetDistPathEnv(""))
	if f1.distPath != IpnsIpfsDist {
		t.Error("did not set default dist path")
	}

	testDist := "/unit/test/dist"
	err := os.Setenv(envIpfsDistPath, testDist)
	if err != nil {
		panic(err)
	}
	defer func() {
		os.Unsetenv(envIpfsDistPath)
	}()

	mf.SetDistPath(GetDistPathEnv(""))
	if f1.distPath != testDist {
		t.Error("did not set dist path from environ")
	}
	if f2.distPath != testDist {
		t.Error("did not set dist path from environ")
	}

	mf.SetDistPath(GetDistPathEnv("ignored"))
	if f1.distPath != testDist {
		t.Error("did not set dist path from environ")
	}
	if f2.distPath != testDist {
		t.Error("did not set dist path from environ")
	}

	testDist = "/unit/test/dist2"
	mf.SetDistPath(testDist)
	if f1.distPath != testDist {
		t.Error("did not set dist path")
	}
	if f2.distPath != testDist {
		t.Error("did not set dist path")
	}
}

func TestHttpFetch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fetcher := NewHttpFetcher()
	ts := createTestServer()
	defer ts.Close()
	err := fetcher.SetGateway(ts.URL)
	if err != nil {
		panic(err)
	}

	rc, err := fetcher.Fetch(ctx, "/versions")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()

	var out []string
	scan := bufio.NewScanner(rc)
	for scan.Scan() {
		out = append(out, scan.Text())
	}
	err = scan.Err()
	if err != nil {
		t.Fatal("could not read versions:", err)
	}

	if len(out) < 6 {
		t.Fatal("do not get all expected data")
	}
	if out[0] != "v1.0.0" {
		t.Fatal("expected v1.0.0 as first line, got", out[0])
	}

	// Check not found
	_, err = fetcher.Fetch(ctx, "/no_such_file")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatal("expected error 404")
	}
}

func TestFetchBinary(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "fetchtest")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmpDir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fetcher := NewHttpFetcher()
	ts := createTestServer()
	defer ts.Close()
	if err = fetcher.SetGateway(ts.URL); err != nil {
		panic(err)
	}

	vers, err := DistVersions(ctx, fetcher, distFSRM, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("latest version of", distFSRM, "is", vers[len(vers)-1])

	bin, err := FetchBinary(ctx, fetcher, distFSRM, vers[0], "", tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	fi, err := os.Stat(bin)
	if os.IsNotExist(err) {
		t.Error("expected file to exist:", bin)
	}

	t.Log("downloaded and unpacked", fi.Size(), "byte file:", fi.Name())

	bin, err = FetchBinary(ctx, fetcher, "go-ipfs", "v0.3.5", "ipfs", tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	fi, err = os.Stat(bin)
	if os.IsNotExist(err) {
		t.Error("expected file to exist:", bin)
	}

	t.Log("downloaded and unpacked", fi.Size(), "byte file:", fi.Name())

	// Check error is destination already exists and is not directory
	_, err = FetchBinary(ctx, fetcher, "go-ipfs", "v0.3.5", "ipfs", bin)
	if !os.IsExist(err) {
		t.Fatal("expected 'exists' error, got", err)
	}

	_, err = FetchBinary(ctx, fetcher, "go-ipfs", "v0.3.5", "ipfs", tmpDir)
	if !os.IsExist(err) {
		t.Error("expected 'exists' error, got:", err)
	}

	os.Remove(path.Join(tmpDir, "ipfs"))

	// Check error creating temp download directory
	err = os.Chmod(tmpDir, 0555)
	if err != nil {
		panic(err)
	}
	err = os.Setenv("TMPDIR", tmpDir)
	if err != nil {
		panic(err)
	}
	_, err = FetchBinary(ctx, fetcher, "go-ipfs", "v0.3.5", "ipfs", tmpDir)
	if !os.IsPermission(err) {
		t.Error("expected 'permission' error, got:", err)
	}
	err = os.Setenv("TMPDIR", "/tmp")
	if err != nil {
		panic(err)
	}
	err = os.Chmod(tmpDir, 0755)
	if err != nil {
		panic(err)
	}

	// Check error if failure to fetch due to bad dist
	_, err = FetchBinary(ctx, fetcher, "not-here", "v0.3.5", "ipfs", tmpDir)
	if err == nil || !strings.Contains(err.Error(), "Not Found") {
		t.Error("expected 'Not Found' error, got:", err)
	}

	// Check error if failure to unpack archive
	_, err = FetchBinary(ctx, fetcher, "go-ipfs", "v0.3.5", "not-such-bin", tmpDir)
	if err == nil || err.Error() != "no binary found in archive" {
		t.Error("expected 'no binary found in archive' error")
	}
}