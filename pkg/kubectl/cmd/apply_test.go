/*
Copyright 2014 The Kubernetes Authors.

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

package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"testing"

	"github.com/spf13/cobra"

	kubeerr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest/fake"
	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/api/annotations"
	"k8s.io/kubernetes/pkg/api/testapi"
	"k8s.io/kubernetes/pkg/apis/extensions"
	cmdtesting "k8s.io/kubernetes/pkg/kubectl/cmd/testing"
	cmdutil "k8s.io/kubernetes/pkg/kubectl/cmd/util"
)

func TestApplyExtraArgsFail(t *testing.T) {
	buf := bytes.NewBuffer([]byte{})
	errBuf := bytes.NewBuffer([]byte{})

	f, _, _, _ := cmdtesting.NewAPIFactory()
	c := NewCmdApply(f, buf, errBuf)
	if validateApplyArgs(c, []string{"rc"}) == nil {
		t.Fatalf("unexpected non-error")
	}
}

func validateApplyArgs(cmd *cobra.Command, args []string) error {
	if len(args) != 0 {
		return cmdutil.UsageError(cmd, "Unexpected args: %v", args)
	}
	return nil
}

const (
	filenameRC            = "../../../test/fixtures/pkg/kubectl/cmd/apply/rc.yaml"
	filenameRCLASTAPPLIED = "../../../test/fixtures/pkg/kubectl/cmd/apply/rc-lastapplied.yaml"
	filenameSVC           = "../../../test/fixtures/pkg/kubectl/cmd/apply/service.yaml"
	filenameRCSVC         = "../../../test/fixtures/pkg/kubectl/cmd/apply/rc-service.yaml"
)

func readBytesFromFile(t *testing.T, filename string) []byte {
	file, err := os.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	data, err := ioutil.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func readReplicationController(t *testing.T, filenameRC string) (string, []byte) {
	rcObj := readReplicationControllerFromFile(t, filenameRC)
	metaAccessor, err := meta.Accessor(rcObj)
	if err != nil {
		t.Fatal(err)
	}
	rcBytes, err := runtime.Encode(testapi.Default.Codec(), rcObj)
	if err != nil {
		t.Fatal(err)
	}

	return metaAccessor.GetName(), rcBytes
}

func readReplicationControllerFromFile(t *testing.T, filename string) *api.ReplicationController {
	data := readBytesFromFile(t, filename)
	rc := api.ReplicationController{}
	if err := runtime.DecodeInto(testapi.Default.Codec(), data, &rc); err != nil {
		t.Fatal(err)
	}

	return &rc
}

func readServiceFromFile(t *testing.T, filename string) *api.Service {
	data := readBytesFromFile(t, filename)
	svc := api.Service{}
	if err := runtime.DecodeInto(testapi.Default.Codec(), data, &svc); err != nil {
		t.Fatal(err)
	}

	return &svc
}

func annotateRuntimeObject(t *testing.T, originalObj, currentObj runtime.Object, kind string) (string, []byte) {
	originalAccessor, err := meta.Accessor(originalObj)
	if err != nil {
		t.Fatal(err)
	}

	originalLabels := originalAccessor.GetLabels()
	originalLabels["DELETE_ME"] = "DELETE_ME"
	originalAccessor.SetLabels(originalLabels)
	original, err := runtime.Encode(testapi.Default.Codec(), originalObj)
	if err != nil {
		t.Fatal(err)
	}

	currentAccessor, err := meta.Accessor(currentObj)
	if err != nil {
		t.Fatal(err)
	}

	currentAnnotations := currentAccessor.GetAnnotations()
	if currentAnnotations == nil {
		currentAnnotations = make(map[string]string)
	}
	currentAnnotations[annotations.LastAppliedConfigAnnotation] = string(original)
	currentAccessor.SetAnnotations(currentAnnotations)
	current, err := runtime.Encode(testapi.Default.Codec(), currentObj)
	if err != nil {
		t.Fatal(err)
	}

	return currentAccessor.GetName(), current
}

func readAndAnnotateReplicationController(t *testing.T, filename string) (string, []byte) {
	rc1 := readReplicationControllerFromFile(t, filename)
	rc2 := readReplicationControllerFromFile(t, filename)
	return annotateRuntimeObject(t, rc1, rc2, "ReplicationController")
}

func readAndAnnotateService(t *testing.T, filename string) (string, []byte) {
	svc1 := readServiceFromFile(t, filename)
	svc2 := readServiceFromFile(t, filename)
	return annotateRuntimeObject(t, svc1, svc2, "Service")
}

func validatePatchApplication(t *testing.T, req *http.Request) {
	patch, err := ioutil.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}

	patchMap := map[string]interface{}{}
	if err := json.Unmarshal(patch, &patchMap); err != nil {
		t.Fatal(err)
	}

	annotationsMap := walkMapPath(t, patchMap, []string{"metadata", "annotations"})
	if _, ok := annotationsMap[annotations.LastAppliedConfigAnnotation]; !ok {
		t.Fatalf("patch does not contain annotation:\n%s\n", patch)
	}

	labelMap := walkMapPath(t, patchMap, []string{"metadata", "labels"})
	if deleteMe, ok := labelMap["DELETE_ME"]; !ok || deleteMe != nil {
		t.Fatalf("patch does not remove deleted key: DELETE_ME:\n%s\n", patch)
	}
}

func walkMapPath(t *testing.T, start map[string]interface{}, path []string) map[string]interface{} {
	finish := start
	for i := 0; i < len(path); i++ {
		var ok bool
		finish, ok = finish[path[i]].(map[string]interface{})
		if !ok {
			t.Fatalf("key:%s of path:%v not found in map:%v", path[i], path, start)
		}
	}

	return finish
}

func TestRunApplyViewLastApplied(t *testing.T) {
	_, rcBytesWithConfig := readReplicationController(t, filenameRCLASTAPPLIED)
	nameRC, rcBytes := readReplicationController(t, filenameRC)
	pathRC := "/namespaces/test/replicationcontrollers/" + nameRC

	tests := []struct {
		name, nameRC, pathRC, filePath, outputFormat, expectedErr, expectedOut, selector string
		args                                                                             []string
		respBytes                                                                        []byte
	}{
		{
			name:         "view with file",
			filePath:     filenameRC,
			outputFormat: "",
			expectedErr:  "",
			expectedOut:  "test: 1234\n",
			selector:     "",
			args:         []string{},
			respBytes:    rcBytesWithConfig,
		},
		{
			name:         "view with file json format",
			filePath:     filenameRC,
			outputFormat: "json",
			expectedErr:  "",
			expectedOut:  "{\n  \"test\": 1234\n}\n",
			selector:     "",
			args:         []string{},
			respBytes:    rcBytesWithConfig,
		},
		{
			name:         "view resource/name invalid format",
			filePath:     "",
			outputFormat: "wide",
			expectedErr:  "error: Unexpected -o output mode: wide, the flag 'output' must be one of yaml|json\nSee 'view-last-applied -h' for help and examples.",
			expectedOut:  "",
			selector:     "",
			args:         []string{"rc", "test-rc"},
			respBytes:    rcBytesWithConfig,
		},
		{
			name:         "view resource with label",
			filePath:     "",
			outputFormat: "",
			expectedErr:  "",
			expectedOut:  "test: 1234\n",
			selector:     "name=test-rc",
			args:         []string{"rc"},
			respBytes:    rcBytesWithConfig,
		},
		{
			name:         "view resource without annotations",
			filePath:     "",
			outputFormat: "",
			expectedErr:  "error: no last-applied-configuration annotation found on resource: test-rc",
			expectedOut:  "",
			selector:     "",
			args:         []string{"rc", "test-rc"},
			respBytes:    rcBytes,
		},
		{
			name:         "view resource no match",
			filePath:     "",
			outputFormat: "",
			expectedErr:  "Error from server (NotFound): the server could not find the requested resource (get replicationcontrollers no-match)",
			expectedOut:  "",
			selector:     "",
			args:         []string{"rc", "no-match"},
			respBytes:    nil,
		},
	}
	for _, test := range tests {
		f, tf, codec, _ := cmdtesting.NewAPIFactory()
		tf.Printer = &testPrinter{}
		tf.UnstructuredClient = &fake.RESTClient{
			APIRegistry:          api.Registry,
			NegotiatedSerializer: unstructuredSerializer,
			Client: fake.CreateHTTPClient(func(req *http.Request) (*http.Response, error) {
				switch p, m := req.URL.Path, req.Method; {
				case p == pathRC && m == "GET":
					bodyRC := ioutil.NopCloser(bytes.NewReader(test.respBytes))
					return &http.Response{StatusCode: 200, Header: defaultHeader(), Body: bodyRC}, nil
				case p == "/namespaces/test/replicationcontrollers" && m == "GET":
					bodyRC := ioutil.NopCloser(bytes.NewReader(test.respBytes))
					return &http.Response{StatusCode: 200, Header: defaultHeader(), Body: bodyRC}, nil
				case p == "/namespaces/test/replicationcontrollers/no-match" && m == "GET":
					return &http.Response{StatusCode: 404, Header: defaultHeader(), Body: objBody(codec, &api.Pod{})}, nil
				case p == "/api/v1/namespaces/test" && m == "GET":
					return &http.Response{StatusCode: 200, Header: defaultHeader(), Body: objBody(codec, &api.Namespace{})}, nil
				default:
					t.Fatalf("unexpected request: %#v\n%#v", req.URL, req)
					return nil, nil
				}
			}),
		}
		tf.Namespace = "test"
		tf.ClientConfig = defaultClientConfig()
		buf, errBuf := bytes.NewBuffer([]byte{}), bytes.NewBuffer([]byte{})

		cmdutil.BehaviorOnFatal(func(str string, code int) {
			if str != test.expectedErr {
				t.Errorf("%s: unexpected error: %s\nexpected: %s", test.name, str, test.expectedErr)
			}
		})

		cmd := NewCmdApplyViewLastApplied(f, buf, errBuf)
		if test.filePath != "" {
			cmd.Flags().Set("filename", test.filePath)
		}
		if test.outputFormat != "" {
			cmd.Flags().Set("output", test.outputFormat)
		}
		if test.selector != "" {
			cmd.Flags().Set("selector", test.selector)
		}

		cmd.Run(cmd, test.args)
		if buf.String() != test.expectedOut {
			t.Fatalf("%s: unexpected output: %s\nexpected: %s", test.name, buf.String(), test.expectedOut)
		}
	}
}

func TestApplyObjectWithoutAnnotation(t *testing.T) {
	initTestErrorHandler(t)
	nameRC, rcBytes := readReplicationController(t, filenameRC)
	pathRC := "/namespaces/test/replicationcontrollers/" + nameRC

	f, tf, _, _ := cmdtesting.NewAPIFactory()
	tf.Printer = &testPrinter{}
	tf.UnstructuredClient = &fake.RESTClient{
		APIRegistry:          api.Registry,
		NegotiatedSerializer: unstructuredSerializer,
		Client: fake.CreateHTTPClient(func(req *http.Request) (*http.Response, error) {
			switch p, m := req.URL.Path, req.Method; {
			case p == pathRC && m == "GET":
				bodyRC := ioutil.NopCloser(bytes.NewReader(rcBytes))
				return &http.Response{StatusCode: 200, Header: defaultHeader(), Body: bodyRC}, nil
			case p == pathRC && m == "PATCH":
				bodyRC := ioutil.NopCloser(bytes.NewReader(rcBytes))
				return &http.Response{StatusCode: 200, Header: defaultHeader(), Body: bodyRC}, nil
			default:
				t.Fatalf("unexpected request: %#v\n%#v", req.URL, req)
				return nil, nil
			}
		}),
	}
	tf.Namespace = "test"
	tf.ClientConfig = defaultClientConfig()
	buf := bytes.NewBuffer([]byte{})
	errBuf := bytes.NewBuffer([]byte{})

	cmd := NewCmdApply(f, buf, errBuf)
	cmd.Flags().Set("filename", filenameRC)
	cmd.Flags().Set("output", "name")
	cmd.Run(cmd, []string{})

	// uses the name from the file, not the response
	expectRC := "replicationcontroller/" + nameRC + "\n"
	expectWarning := warningNoLastAppliedConfigAnnotation
	if errBuf.String() != expectWarning {
		t.Fatalf("unexpected non-warning: %s\nexpected: %s", errBuf.String(), expectWarning)
	}
	if buf.String() != expectRC {
		t.Fatalf("unexpected output: %s\nexpected: %s", buf.String(), expectRC)
	}
}

func TestApplyObject(t *testing.T) {
	initTestErrorHandler(t)
	nameRC, currentRC := readAndAnnotateReplicationController(t, filenameRC)
	pathRC := "/namespaces/test/replicationcontrollers/" + nameRC

	f, tf, _, _ := cmdtesting.NewAPIFactory()
	tf.Printer = &testPrinter{}
	tf.UnstructuredClient = &fake.RESTClient{
		APIRegistry:          api.Registry,
		NegotiatedSerializer: unstructuredSerializer,
		Client: fake.CreateHTTPClient(func(req *http.Request) (*http.Response, error) {
			switch p, m := req.URL.Path, req.Method; {
			case p == pathRC && m == "GET":
				bodyRC := ioutil.NopCloser(bytes.NewReader(currentRC))
				return &http.Response{StatusCode: 200, Header: defaultHeader(), Body: bodyRC}, nil
			case p == pathRC && m == "PATCH":
				validatePatchApplication(t, req)
				bodyRC := ioutil.NopCloser(bytes.NewReader(currentRC))
				return &http.Response{StatusCode: 200, Header: defaultHeader(), Body: bodyRC}, nil
			default:
				t.Fatalf("unexpected request: %#v\n%#v", req.URL, req)
				return nil, nil
			}
		}),
	}
	tf.Namespace = "test"
	buf := bytes.NewBuffer([]byte{})
	errBuf := bytes.NewBuffer([]byte{})

	cmd := NewCmdApply(f, buf, errBuf)
	cmd.Flags().Set("filename", filenameRC)
	cmd.Flags().Set("output", "name")
	cmd.Run(cmd, []string{})

	// uses the name from the file, not the response
	expectRC := "replicationcontroller/" + nameRC + "\n"
	if buf.String() != expectRC {
		t.Fatalf("unexpected output: %s\nexpected: %s", buf.String(), expectRC)
	}
}

func TestApplyRetry(t *testing.T) {
	initTestErrorHandler(t)
	nameRC, currentRC := readAndAnnotateReplicationController(t, filenameRC)
	pathRC := "/namespaces/test/replicationcontrollers/" + nameRC

	firstPatch := true
	retry := false
	getCount := 0
	f, tf, _, _ := cmdtesting.NewAPIFactory()
	tf.Printer = &testPrinter{}
	tf.UnstructuredClient = &fake.RESTClient{
		APIRegistry:          api.Registry,
		NegotiatedSerializer: unstructuredSerializer,
		Client: fake.CreateHTTPClient(func(req *http.Request) (*http.Response, error) {
			switch p, m := req.URL.Path, req.Method; {
			case p == pathRC && m == "GET":
				getCount++
				bodyRC := ioutil.NopCloser(bytes.NewReader(currentRC))
				return &http.Response{StatusCode: 200, Header: defaultHeader(), Body: bodyRC}, nil
			case p == pathRC && m == "PATCH":
				if firstPatch {
					firstPatch = false
					statusErr := kubeerr.NewConflict(schema.GroupResource{Group: "", Resource: "rc"}, "test-rc", fmt.Errorf("the object has been modified. Please apply at first."))
					bodyBytes, _ := json.Marshal(statusErr)
					bodyErr := ioutil.NopCloser(bytes.NewReader(bodyBytes))
					return &http.Response{StatusCode: http.StatusConflict, Header: defaultHeader(), Body: bodyErr}, nil
				}
				retry = true
				validatePatchApplication(t, req)
				bodyRC := ioutil.NopCloser(bytes.NewReader(currentRC))
				return &http.Response{StatusCode: 200, Header: defaultHeader(), Body: bodyRC}, nil
			default:
				t.Fatalf("unexpected request: %#v\n%#v", req.URL, req)
				return nil, nil
			}
		}),
	}
	tf.Namespace = "test"
	buf := bytes.NewBuffer([]byte{})
	errBuf := bytes.NewBuffer([]byte{})

	cmd := NewCmdApply(f, buf, errBuf)
	cmd.Flags().Set("filename", filenameRC)
	cmd.Flags().Set("output", "name")
	cmd.Run(cmd, []string{})

	if !retry || getCount != 2 {
		t.Fatalf("apply didn't retry when get conflict error")
	}

	// uses the name from the file, not the response
	expectRC := "replicationcontroller/" + nameRC + "\n"
	if buf.String() != expectRC {
		t.Fatalf("unexpected output: %s\nexpected: %s", buf.String(), expectRC)
	}
}

func TestApplyNonExistObject(t *testing.T) {
	nameRC, currentRC := readAndAnnotateReplicationController(t, filenameRC)
	pathRC := "/namespaces/test/replicationcontrollers"
	pathNameRC := pathRC + "/" + nameRC

	f, tf, _, _ := cmdtesting.NewAPIFactory()
	tf.Printer = &testPrinter{}
	tf.UnstructuredClient = &fake.RESTClient{
		APIRegistry:          api.Registry,
		NegotiatedSerializer: unstructuredSerializer,
		Client: fake.CreateHTTPClient(func(req *http.Request) (*http.Response, error) {
			switch p, m := req.URL.Path, req.Method; {
			case p == "/api/v1/namespaces/test" && m == "GET":
				return &http.Response{StatusCode: 404, Header: defaultHeader(), Body: ioutil.NopCloser(bytes.NewReader(nil))}, nil
			case p == pathNameRC && m == "GET":
				return &http.Response{StatusCode: 404, Header: defaultHeader(), Body: ioutil.NopCloser(bytes.NewReader(nil))}, nil
			case p == pathRC && m == "POST":
				bodyRC := ioutil.NopCloser(bytes.NewReader(currentRC))
				return &http.Response{StatusCode: 201, Header: defaultHeader(), Body: bodyRC}, nil
			default:
				t.Fatalf("unexpected request: %#v\n%#v", req.URL, req)
				return nil, nil
			}
		}),
	}
	tf.Namespace = "test"
	buf := bytes.NewBuffer([]byte{})
	errBuf := bytes.NewBuffer([]byte{})

	cmd := NewCmdApply(f, buf, errBuf)
	cmd.Flags().Set("filename", filenameRC)
	cmd.Flags().Set("output", "name")
	cmd.Run(cmd, []string{})

	// uses the name from the file, not the response
	expectRC := "replicationcontroller/" + nameRC + "\n"
	if buf.String() != expectRC {
		t.Errorf("unexpected output: %s\nexpected: %s", buf.String(), expectRC)
	}
}

func TestApplyMultipleObjectsAsList(t *testing.T) {
	testApplyMultipleObjects(t, true)
}

func TestApplyMultipleObjectsAsFiles(t *testing.T) {
	testApplyMultipleObjects(t, false)
}

func testApplyMultipleObjects(t *testing.T, asList bool) {
	nameRC, currentRC := readAndAnnotateReplicationController(t, filenameRC)
	pathRC := "/namespaces/test/replicationcontrollers/" + nameRC

	nameSVC, currentSVC := readAndAnnotateService(t, filenameSVC)
	pathSVC := "/namespaces/test/services/" + nameSVC

	f, tf, _, _ := cmdtesting.NewAPIFactory()
	tf.Printer = &testPrinter{}
	tf.UnstructuredClient = &fake.RESTClient{
		APIRegistry:          api.Registry,
		NegotiatedSerializer: unstructuredSerializer,
		Client: fake.CreateHTTPClient(func(req *http.Request) (*http.Response, error) {
			switch p, m := req.URL.Path, req.Method; {
			case p == pathRC && m == "GET":
				bodyRC := ioutil.NopCloser(bytes.NewReader(currentRC))
				return &http.Response{StatusCode: 200, Header: defaultHeader(), Body: bodyRC}, nil
			case p == pathRC && m == "PATCH":
				validatePatchApplication(t, req)
				bodyRC := ioutil.NopCloser(bytes.NewReader(currentRC))
				return &http.Response{StatusCode: 200, Header: defaultHeader(), Body: bodyRC}, nil
			case p == pathSVC && m == "GET":
				bodySVC := ioutil.NopCloser(bytes.NewReader(currentSVC))
				return &http.Response{StatusCode: 200, Header: defaultHeader(), Body: bodySVC}, nil
			case p == pathSVC && m == "PATCH":
				validatePatchApplication(t, req)
				bodySVC := ioutil.NopCloser(bytes.NewReader(currentSVC))
				return &http.Response{StatusCode: 200, Header: defaultHeader(), Body: bodySVC}, nil
			default:
				t.Fatalf("unexpected request: %#v\n%#v", req.URL, req)
				return nil, nil
			}
		}),
	}
	tf.Namespace = "test"
	buf := bytes.NewBuffer([]byte{})
	errBuf := bytes.NewBuffer([]byte{})

	cmd := NewCmdApply(f, buf, errBuf)
	if asList {
		cmd.Flags().Set("filename", filenameRCSVC)
	} else {
		cmd.Flags().Set("filename", filenameRC)
		cmd.Flags().Set("filename", filenameSVC)
	}
	cmd.Flags().Set("output", "name")

	cmd.Run(cmd, []string{})

	// Names should come from the REST response, NOT the files
	expectRC := "replicationcontroller/" + nameRC + "\n"
	expectSVC := "service/" + nameSVC + "\n"
	// Test both possible orders since output is non-deterministic.
	expectOne := expectRC + expectSVC
	expectTwo := expectSVC + expectRC
	if buf.String() != expectOne && buf.String() != expectTwo {
		t.Fatalf("unexpected output: %s\nexpected: %s OR %s", buf.String(), expectOne, expectTwo)
	}
}

const (
	filenameDeployObjServerside = "../../../test/fixtures/pkg/kubectl/cmd/apply/deploy-serverside.yaml"
	filenameDeployObjClientside = "../../../test/fixtures/pkg/kubectl/cmd/apply/deploy-clientside.yaml"
)

func readDeploymentFromFile(t *testing.T, file string) []byte {
	raw := readBytesFromFile(t, file)
	obj := &extensions.Deployment{}
	if err := runtime.DecodeInto(testapi.Extensions.Codec(), raw, obj); err != nil {
		t.Fatal(err)
	}
	objJSON, err := runtime.Encode(testapi.Extensions.Codec(), obj)
	if err != nil {
		t.Fatal(err)
	}
	return objJSON
}

func TestApplyNULLPreservation(t *testing.T) {
	initTestErrorHandler(t)
	deploymentName := "nginx-deployment"
	deploymentPath := "/namespaces/test/deployments/" + deploymentName

	verifiedPatch := false
	deploymentBytes := readDeploymentFromFile(t, filenameDeployObjServerside)

	f, tf, _, _ := cmdtesting.NewTestFactory()
	tf.UnstructuredClient = &fake.RESTClient{
		APIRegistry:          api.Registry,
		NegotiatedSerializer: unstructuredSerializer,
		Client: fake.CreateHTTPClient(func(req *http.Request) (*http.Response, error) {
			switch p, m := req.URL.Path, req.Method; {
			case p == deploymentPath && m == "GET":
				body := ioutil.NopCloser(bytes.NewReader(deploymentBytes))
				return &http.Response{StatusCode: 200, Header: defaultHeader(), Body: body}, nil
			case p == deploymentPath && m == "PATCH":
				patch, err := ioutil.ReadAll(req.Body)
				if err != nil {
					t.Fatal(err)
				}

				patchMap := map[string]interface{}{}
				if err := json.Unmarshal(patch, &patchMap); err != nil {
					t.Fatal(err)
				}
				annotationMap := walkMapPath(t, patchMap, []string{"metadata", "annotations"})
				if _, ok := annotationMap[annotations.LastAppliedConfigAnnotation]; !ok {
					t.Fatalf("patch does not contain annotation:\n%s\n", patch)
				}
				strategy := walkMapPath(t, patchMap, []string{"spec", "strategy"})
				if value, ok := strategy["rollingUpdate"]; !ok || value != nil {
					t.Fatalf("patch did not retain null value in key: rollingUpdate:\n%s\n", patch)
				}
				verifiedPatch = true

				// The real API server would had returned the patched object but Kubectl
				// is ignoring the actual return object.
				// TODO: Make this match actual server behavior by returning the patched object.
				body := ioutil.NopCloser(bytes.NewReader(deploymentBytes))
				return &http.Response{StatusCode: 200, Header: defaultHeader(), Body: body}, nil
			default:
				t.Fatalf("unexpected request: %#v\n%#v", req.URL, req)
				return nil, nil
			}
		}),
	}
	tf.Namespace = "test"
	tf.ClientConfig = defaultClientConfig()
	buf := bytes.NewBuffer([]byte{})
	errBuf := bytes.NewBuffer([]byte{})

	cmd := NewCmdApply(f, buf, errBuf)
	cmd.Flags().Set("filename", filenameDeployObjClientside)
	cmd.Flags().Set("output", "name")

	cmd.Run(cmd, []string{})

	expected := "deployment/" + deploymentName + "\n"
	if buf.String() != expected {
		t.Fatalf("unexpected output: %s\nexpected: %s", buf.String(), expected)
	}

	if !verifiedPatch {
		t.Fatal("No server-side patch call detected")
	}
}
