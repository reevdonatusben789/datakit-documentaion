// Unless explicitly stated otherwise all files in this repository are licensed
// under the MIT License.
// This product includes software developed at Guance Cloud (https://www.guance.com/).
// Copyright 2021-present Guance, Inc.

package pythond

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/gin-gonic/gin"
	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
	"github.com/stretchr/testify/assert"
	"gitlab.jiagouyun.com/cloudcare-tools/datakit/internal/testutils"
	dkio "gitlab.jiagouyun.com/cloudcare-tools/datakit/io"
	"gitlab.jiagouyun.com/cloudcare-tools/datakit/plugins/inputs"
)

func TestPythondInput(t *testing.T) {
	start := time.Now()
	cases, err := buildCases(t)
	if err != nil {
		cr := &testutils.CaseResult{
			Name:          t.Name(),
			Status:        testutils.TestPassed,
			FailedMessage: err.Error(),
			Cost:          time.Since(start),
		}

		_ = testutils.Flush(cr)
		return
	}

	t.Logf("testing %d cases...", len(cases))

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			caseStart := time.Now()

			t.Logf("testing %s...", tc.name)

			if err := tc.run(); err != nil {
				tc.cr.Status = testutils.TestFailed
				tc.cr.FailedMessage = err.Error()

				assert.NoError(t, err)
			} else {
				tc.cr.Status = testutils.TestPassed
			}

			tc.cr.Cost = time.Since(caseStart)

			assert.NoError(t, testutils.Flush(tc.cr))

			t.Cleanup(func() {
				// clean remote docker resources
				if tc.resource == nil {
					return
				}

				assert.NoError(t, tc.pool.Purge(tc.resource))
			})
		})
	}
}

func buildCases(t *testing.T) ([]*caseSpec, error) {
	t.Helper()

	remote := testutils.GetRemote()

	bases := []struct {
		name           string // Also used as build image name:tag.
		conf           string
		dockerFileText string // Empty if not build image.
		exposedPorts   []string
		opts           []inputs.PointCheckOption
	}{
		{
			name: "pubrepo.jiagouyun.com/image-repo-for-testing/python:3-datakit_framework",
		},
	}

	var cases []*caseSpec

	// compose cases
	for _, base := range bases {
		feeder := dkio.NewMockedFeeder()

		ipt := defaultInput()
		ipt.feeder = feeder

		_, err := toml.Decode(base.conf, ipt)
		assert.NoError(t, err)

		repoTag := strings.Split(base.name, ":")

		cases = append(cases, &caseSpec{
			t:       t,
			ipt:     ipt,
			name:    base.name,
			feeder:  feeder,
			repo:    repoTag[0],
			repoTag: repoTag[1],

			dockerFileText: base.dockerFileText,
			exposedPorts:   base.exposedPorts,
			opts:           base.opts,

			cr: &testutils.CaseResult{
				Name:        t.Name(),
				Case:        base.name,
				ExtraFields: map[string]any{},
				ExtraTags: map[string]string{
					"image":       repoTag[0],
					"image_tag":   repoTag[1],
					"docker_host": remote.Host,
					"docker_port": remote.Port,
				},
			},
		})
	}

	return cases, nil
}

////////////////////////////////////////////////////////////////////////////////

// caseSpec.

type caseSpec struct {
	t *testing.T

	name           string
	repo           string
	repoTag        string
	dockerFileText string
	exposedPorts   []string
	opts           []inputs.PointCheckOption

	ipt    *Input
	feeder *dkio.MockedFeeder

	pool     *dockertest.Pool
	resource *dockertest.Resource

	cr *testutils.CaseResult
}

var (
	done      chan struct{}
	lock      sync.RWMutex
	errorMsgs []string
	count     uint32
)

type FeedMeasurementBody []struct {
	Measurement string                 `json:"measurement"`
	Tags        map[string]string      `json:"tags"`
	Fields      map[string]interface{} `json:"fields"`
}

func addErrorMsgs(str string) {
	lock.RLock()
	defer lock.RUnlock()
	errorMsgs = append(errorMsgs, str)
}

func (cs *caseSpec) addErrorMsgs(str string) {
	addErrorMsgs(str)
}

func (cs *caseSpec) handler(c *gin.Context) {
	uri, err := url.ParseRequestURI(c.Request.URL.RequestURI())
	if err != nil {
		cs.t.Logf("%s", err.Error())
		return
	}

	body, err := ioutil.ReadAll(c.Request.Body)
	defer c.Request.Body.Close()
	if err != nil {
		cs.t.Logf("%s", err.Error())
		return
	}
	str := string(body)
	cs.t.Logf(str)
	cs.t.Logf("\n")

	switch uri.Path {
	case "/v1/write/metrics":
	case "/v1/write/metric":
		atomic.AddUint32(&count, 1)
		if uri.RawQuery == "input=py_from_docker" {
			if str != `[{"measurement": "measurement1", "tags": {"tag_name": "tag_value"}, "fields": {"count": 1}}]` {
				cs.addErrorMsgs("[ERROR] 10001")
			}
		} else {
			if str != `[{"measurement": "measurement2", "tags": {"tag1": "val1", "tag2": "val2"}, "fields": {"custom_field1": "val1", "custom_field2": 1000, "custom_key1": "custom_value1", "custom_key2": "custom_value2", "custom_key3": "custom_value3"}, "time": null}]` {
				cs.addErrorMsgs("[ERROR] 10002")
			}
		}
	case "/v1/write/network":
	case "/v1/write/keyevent":
		atomic.AddUint32(&count, 1)
		parsedBody := FeedMeasurementBody{}
		if err := json.Unmarshal(body, &parsedBody); err != nil {
			cs.t.Logf("json.Unmarshal failed: %s", err.Error())
			return
		}
		if len(parsedBody) == 0 {
			cs.t.Logf("parse body failed: body length 0")
			return
		}
		src, ok := parsedBody[0].Fields["df_source"]
		if !ok {
			cs.t.Logf("parse body failed: no df_source")
			return
		}
		srcStr, ok := src.(string)
		if !ok {
			cs.t.Logf("parse body failed: df_source not string")
			return
		}
		switch srcStr {
		case "user":
			if str != `[{"measurement": "measurement", "tags": {"tag1": "val1", "tag2": "val2"}, "fields": {"df_date_range": 10, "df_source": "user", "df_user_id": "user_id", "df_status": "info", "df_event_id": "event_id", "df_title": "title", "df_message": "message", "custom_key1": "custom_value1", "custom_key2": "custom_value2", "custom_key3": "custom_value3"}}]` {
				cs.addErrorMsgs("[ERROR] 10007")
			}
		case "monitor":
			if str != `[{"measurement": "measurement", "tags": {"tag1": "val1", "tag2": "val2"}, "fields": {"df_date_range": 10, "df_source": "monitor", "df_dimension_tags": "{\"host\":\"web01\"}", "df_status": "info", "df_event_id": "event_id", "df_title": "title", "df_message": "message", "custom_key1": "custom_value1", "custom_key2": "custom_value2", "custom_key3": "custom_value3"}}]` {
				cs.addErrorMsgs("[ERROR] 10005")
			}
		case "system":
			if str != `[{"measurement": "measurement", "tags": {"tag1": "val1", "tag2": "val2"}, "fields": {"df_date_range": 10, "df_source": "system", "df_status": "info", "df_event_id": "event_id", "df_title": "feed_system_event", "df_message": "message", "custom_key1": "custom_value1", "custom_key2": "custom_value2", "custom_key3": "custom_value3"}}]` {
				cs.addErrorMsgs("[ERROR] 10006")
			}
		}
	case "/v1/write/object":
		atomic.AddUint32(&count, 1)
		if str != `[{"measurement": "measurement4", "tags": {"tag1": "val1", "tag2": "val2", "name": "name"}, "fields": {"custom_field1": "val1", "custom_field2": 1000, "custom_key1": "custom_value1", "custom_key2": "custom_value2", "custom_key3": "custom_value3"}, "time": null}]` {
			cs.addErrorMsgs("[ERROR] 10004")
		}
	case "/v1/write/custom_object":
	case "/v1/write/logging":
		atomic.AddUint32(&count, 1)
		if str != `[{"measurement": "measurement3", "tags": {"tag1": "val1", "tag2": "val2"}, "fields": {"message": "This is the message for testing", "custom_key1": "custom_value1", "custom_key2": "custom_value2", "custom_key3": "custom_value3"}, "time": null}]` {
			cs.addErrorMsgs("[ERROR] 10003")
		}
	case "/v1/write/tracing":
	case "/v1/write/rum":
	case "/v1/write/security":
	case "/v1/write/profiling":
	}

	val := atomic.LoadUint32(&count)
	if val == 7 {
		atomic.SwapUint32(&count, 0)
		done <- struct{}{}
	}
}

func (cs *caseSpec) run() error {
	r := testutils.GetRemote()
	dockerTCP := r.TCPURL()

	cs.t.Logf("get remote: %+#v, TCP: %s", r, dockerTCP)

	router := gin.New()
	router.POST("/v1/write/metrics", cs.handler)
	router.POST("/v1/write/metric", cs.handler)
	router.POST("/v1/write/network", cs.handler)
	router.POST("/v1/write/keyevent", cs.handler)
	router.POST("/v1/write/object", cs.handler)
	router.POST("/v1/write/custom_object", cs.handler)
	router.POST("/v1/write/logging", cs.handler)
	router.POST("/v1/write/tracing", cs.handler)
	router.POST("/v1/write/rum", cs.handler)
	router.POST("/v1/write/security", cs.handler)
	router.POST("/v1/write/profiling", cs.handler)

	srv := &http.Server{
		Addr:    ":59529",
		Handler: router,
	}

	go func() {
		done = nil
		done = make(chan struct{})
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			panic(err)
		}
	}()

	start := time.Now()

	p, err := cs.getPool(dockerTCP)
	if err != nil {
		return err
	}

	containerName := cs.getContainterName()

	// Remove the container if exist.
	if err := p.RemoveContainerByName(containerName); err != nil {
		return err
	}

	dockerFileDir, dockerFilePath, err := cs.getDockerFilePath()
	if err != nil {
		return err
	}
	defer os.RemoveAll(dockerFileDir)

	extIP, err := externalIP()
	if err != nil {
		return err
	}

	var resource *dockertest.Resource

	if len(cs.dockerFileText) == 0 {
		// Just run a container from existing docker image.
		resource, err = p.RunWithOptions(
			&dockertest.RunOptions{
				Name: containerName, // ATTENTION: not cs.name.

				Repository: cs.repo,
				Tag:        cs.repoTag,
				Env:        []string{fmt.Sprintf("DATAKIT_HOST=%s", extIP), "DATAKIT_PORT=59529"},

				ExposedPorts: cs.exposedPorts,
				PortBindings: cs.getPortBindings(),
			},

			func(c *docker.HostConfig) {
				c.RestartPolicy = docker.RestartPolicy{Name: "no"}
				c.AutoRemove = true
			},
		)
	} else {
		// Build docker image from Dockerfile and run a container from it.
		resource, err = p.BuildAndRunWithOptions(
			dockerFilePath,

			&dockertest.RunOptions{
				Name: cs.name,

				Repository: cs.repo,
				Tag:        cs.repoTag,
				Env:        []string{fmt.Sprintf("DATAKIT_HOST=%s", extIP), "DATAKIT_PORT=59529"},

				ExposedPorts: cs.exposedPorts,
				PortBindings: cs.getPortBindings(),
			},

			func(c *docker.HostConfig) {
				c.RestartPolicy = docker.RestartPolicy{Name: "no"}
				c.AutoRemove = true
			},
		)
	}

	if err != nil {
		cs.t.Logf("%s", err.Error())
		return err
	}

	cs.pool = p
	cs.resource = resource

	cs.t.Logf("check service(%s:%v)...", r.Host, cs.exposedPorts)

	if err := cs.portsOK(r); err != nil {
		return err
	}

	cs.cr.AddField("container_ready_cost", int64(time.Since(start)))

	tick := time.NewTicker(time.Second * 30)
	out := false
	for {
		if out {
			break
		}

		select {
		case <-tick.C:
			out = true
		case <-done:
			out = true
		}
	}

	if len(errorMsgs) > 0 {
		return fmt.Errorf("errorMsgs: %#v", errorMsgs)
	}
	errorMsgs = errorMsgs[:0]

	cs.t.Logf("exit...")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil && errors.Is(err, http.ErrServerClosed) {
		cs.t.Logf("Shutdown failed: %v", err)
	}

	return nil
}

func (cs *caseSpec) getPool(endpoint string) (*dockertest.Pool, error) {
	p, err := dockertest.NewPool(endpoint)
	if err != nil {
		return nil, err
	}
	err = p.Client.Ping()
	if err != nil {
		cs.t.Logf("Could not connect to Docker: %v", err)
		return nil, err
	}
	return p, nil
}

func (cs *caseSpec) getDockerFilePath() (dirName string, fileName string, err error) {
	tmpDir, err := ioutil.TempDir("", "dockerfiles_")
	if err != nil {
		cs.t.Logf("ioutil.TempDir failed: %s", err.Error())
		return "", "", err
	}

	tmpFile, err := ioutil.TempFile(tmpDir, "dockerfile_")
	if err != nil {
		cs.t.Logf("ioutil.TempFile failed: %s", err.Error())
		return "", "", err
	}

	_, err = tmpFile.WriteString(cs.dockerFileText)
	if err != nil {
		cs.t.Logf("TempFile.WriteString failed: %s", err.Error())
		return "", "", err
	}

	if err := os.Chmod(tmpFile.Name(), os.ModePerm); err != nil {
		cs.t.Logf("os.Chmod failed: %s", err.Error())
		return "", "", err
	}

	if err := tmpFile.Close(); err != nil {
		cs.t.Logf("Close failed: %s", err.Error())
		return "", "", err
	}

	return tmpDir, tmpFile.Name(), nil
}

func (cs *caseSpec) getContainterName() string {
	nameTag := strings.Split(cs.name, ":")
	name := filepath.Base(nameTag[0])
	return name
}

func (cs *caseSpec) getPortBindings() map[docker.Port][]docker.PortBinding {
	portBindings := make(map[docker.Port][]docker.PortBinding)

	for _, v := range cs.exposedPorts {
		portBindings[docker.Port(v)] = []docker.PortBinding{{HostIP: "0.0.0.0", HostPort: docker.Port(v).Port()}}
	}

	return portBindings
}

func (cs *caseSpec) portsOK(r *testutils.RemoteInfo) error {
	for _, v := range cs.exposedPorts {
		if !r.PortOK(docker.Port(v).Port(), time.Minute) {
			return fmt.Errorf("service checking failed")
		}
	}
	return nil
}

////////////////////////////////////////////////////////////////////////////////

func externalIP() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue // interface down
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue // loopback interface
		}
		addrs, err := iface.Addrs()
		if err != nil {
			return "", err
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			ip = ip.To4()
			if ip == nil {
				continue // not an ipv4 address
			}
			return ip.String(), nil
		}
	}
	return "", errors.New("are you connected to the network?")
}
