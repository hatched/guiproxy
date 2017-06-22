package server_test

import (
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/net/websocket"

	it "github.com/juju/guiproxy/internal/testing"
	"github.com/juju/guiproxy/server"
)

func TestNew(t *testing.T) {
	// Set up test servers.
	gui := httptest.NewServer(newGUIServer())
	defer gui.Close()
	guiURL := it.MustParseURL(t, gui.URL)

	juju := httptest.NewTLSServer(newJujuServer())
	defer juju.Close()
	jujuURL := it.MustParseURL(t, juju.URL)

	legacyJuju := httptest.NewTLSServer(newLegacyJujuServer())
	defer legacyJuju.Close()
	legacyJujuURL := it.MustParseURL(t, legacyJuju.URL)

	proxy := httptest.NewServer(server.New(server.Params{
		ControllerAddr: jujuURL.Host,
		ModelUUID:      "example-uuid",
		OriginAddr:     "http://1.2.3.4:4242",
		GUIURL:         guiURL,
	}))
	defer proxy.Close()
	serverURL := it.MustParseURL(t, proxy.URL)

	legacyProxy := httptest.NewServer(server.New(server.Params{
		ControllerAddr: legacyJujuURL.Host,
		ModelUUID:      "example-legacy-uuid",
		OriginAddr:     "http://1.2.3.4:4242",
		GUIURL:         guiURL,
		LegacyJuju:     true,
	}))
	defer proxy.Close()
	legacyServerURL := it.MustParseURL(t, legacyProxy.URL)

	customConfigProxy := httptest.NewServer(server.New(server.Params{
		ControllerAddr: jujuURL.Host,
		ModelUUID:      "",
		OriginAddr:     "http://1.2.3.4:4242",
		GUIURL:         guiURL,
		GUIConfig: map[string]interface{}{
			"answer":          42,
			"baseUrl":         "/",
			"container":       "#different-one",
			"gisf":            true,
			"jujuCoreVersion": "42.47.0",
		},
	}))
	defer customConfigProxy.Close()
	customConfigServerURL := it.MustParseURL(t, customConfigProxy.URL)

	jujuParts := strings.Split(jujuURL.Host, ":")
	controllerPath := fmt.Sprintf("/controller/%s/%s/controller-api", jujuParts[0], jujuParts[1])
	modelPath1 := fmt.Sprintf("/model/%s/%s/uuid/model-api", jujuParts[0], jujuParts[1])
	modelPath2 := fmt.Sprintf("/model/%s/%s/another-uuid/model-api", jujuParts[0], jujuParts[1])

	legacyJujuParts := strings.Split(legacyJujuURL.Host, ":")
	legacyModelPath := fmt.Sprintf("/model/%s/%s/model-api", legacyJujuParts[0], legacyJujuParts[1])

	t.Run("testJujuWebSocket Controller", testJujuWebSocket(serverURL, "/api", controllerPath))
	t.Run("testJujuWebSocket Model1", testJujuWebSocket(serverURL, "/model/uuid/api", modelPath1))
	t.Run("testJujuWebSocket Model2", testJujuWebSocket(serverURL, "/model/another-uuid/api", modelPath2))
	t.Run("testJujuWebSocket Legacy", testJujuWebSocket(legacyServerURL, "/", legacyModelPath))

	t.Run("testJujuHTTPS", testJujuHTTPS(serverURL))
	t.Run("testJujuHTTPS Legacy", testJujuHTTPS(legacyServerURL))

	t.Run("testGUIConfig", testGUIConfig(
		serverURL,
		fmt.Sprintf(`"controllerSocketTemplate": "%s"`, server.ControllerSrcTemplate),
		fmt.Sprintf(`"socketTemplate": "%s"`, server.ModelSrcTemplate),
		fmt.Sprintf(`"apiAddress": "%s"`, jujuURL.Host),
		fmt.Sprintf(`"jujuCoreVersion": "%s"`, server.JujuVersion),
		`"jujuEnvUUID": "example-uuid"`,
		`"gisf": false`,
	))
	t.Run("testGUIConfig Legacy", testGUIConfig(
		legacyServerURL,
		`"controllerSocketTemplate": ""`,
		fmt.Sprintf(`"socketTemplate": "%s"`, server.LegacyModelSrcTemplate),
		fmt.Sprintf(`"apiAddress": "%s"`, legacyJujuURL.Host),
		fmt.Sprintf(`"jujuCoreVersion": "%s"`, server.LegacyJujuVersion),
		`"jujuEnvUUID": "example-legacy-uuid"`,
	))
	t.Run("testGUIConfig Customized", testGUIConfig(
		customConfigServerURL,
		fmt.Sprintf(`"controllerSocketTemplate": "%s"`, server.ControllerSrcTemplate),
		fmt.Sprintf(`"socketTemplate": "%s"`, server.ModelSrcTemplate),
		fmt.Sprintf(`"apiAddress": "%s"`, jujuURL.Host),
		`"answer": 42`,
		`"baseUrl": "/"`,
		`"container": "#different-one"`,
		`"gisf": true`,
		`"jujuCoreVersion": "42.47.0"`,
		`"jujuEnvUUID": ""`,
	))

	t.Run("testGUIStaticFiles", testGUIStaticFiles(serverURL))
	t.Run("testGUIStaticFiles Legacy", testGUIStaticFiles(legacyServerURL))
}

func testJujuWebSocket(serverURL *url.URL, dstPath, srcPath string) func(t *testing.T) {
	origin := "http://localhost/"
	u := *serverURL
	u.Scheme = "ws"
	socketURL := u.String() + srcPath
	return func(t *testing.T) {
		// Connect to the remote WebSocket.
		ws, err := websocket.Dial(socketURL, "", origin)
		it.AssertError(t, err, nil)
		defer ws.Close()
		// Send a message.
		msg := jsonMessage{
			Request: "my api request",
		}
		err = websocket.JSON.Send(ws, msg)
		it.AssertError(t, err, nil)
		// Retrieve the response from the WebSocket server.
		err = websocket.JSON.Receive(ws, &msg)
		it.AssertError(t, err, nil)
		it.AssertString(t, msg.Request, "my api request")
		it.AssertString(t, msg.Response, dstPath)
	}
}

func testJujuHTTPS(serverURL *url.URL) func(t *testing.T) {
	return func(t *testing.T) {
		// Make the HTTP request to retrieve a Juju HTTPS API endpoint.
		resp, err := http.Get(serverURL.String() + "/juju-core/api/path")
		it.AssertError(t, err, nil)
		defer resp.Body.Close()
		// The request succeeded.
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("invalid response code from Juju endpoint: %v", resp.StatusCode)
		}
		// The response body includes the expected content.
		b, err := ioutil.ReadAll(resp.Body)
		it.AssertError(t, err, nil)
		it.AssertString(t, string(b), "juju: /api/path")
	}
}

func testGUIConfig(serverURL *url.URL, fragments ...string) func(t *testing.T) {
	return func(t *testing.T) {
		// Make the HTTP request to retrieve the GUI configuration file.
		resp, err := http.Get(serverURL.String() + "/config.js")
		it.AssertError(t, err, nil)
		defer resp.Body.Close()
		// The request succeeded.
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("invalid response code from config.js: %v", resp.StatusCode)
		}
		// The response body includes all the provided fragments.
		b, err := ioutil.ReadAll(resp.Body)
		it.AssertError(t, err, nil)
		cfg := string(b)
		for _, fragment := range fragments {
			if !strings.Contains(cfg, fragment) {
				t.Fatalf("invalid GUI config: %q not included in %q", fragment, cfg)
			}
		}
	}
}

func testGUIStaticFiles(serverURL *url.URL) func(t *testing.T) {
	return func(t *testing.T) {
		// Make the HTTP request to retrieve a GUI static file.
		resp, err := http.Get(serverURL.String() + "/my/path")
		it.AssertError(t, err, nil)
		defer resp.Body.Close()
		// The request succeeded.
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("invalid response code from GUI static file: %v", resp.StatusCode)
		}
		// The response body includes the expected content.
		b, err := ioutil.ReadAll(resp.Body)
		it.AssertError(t, err, nil)
		it.AssertString(t, string(b), "gui: /my/path")
	}
}

// newGUIServer creates and returns a new test server simulating a remote Juju
// GUI run in sandbox mode.
func newGUIServer() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		io.WriteString(w, "gui: "+req.URL.Path)
	})
	return mux
}

// newJujuServer creates and returns a new test server simulating a remote Juju
// controller and model.
func newJujuServer() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api", websocket.Handler(echoHandler))
	mux.Handle("/model/", websocket.Handler(echoHandler))
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		io.WriteString(w, "juju: "+req.URL.Path)
	})
	return mux
}

// newLegacyJujuServer creates and returns a new test server simulating a
// remote Juju 1 model.
func newLegacyJujuServer() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", websocket.Handler(echoHandler))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, req *http.Request) {
		io.WriteString(w, "juju: "+req.URL.Path)
	})
	return mux
}

// echoHandler is a WebSocket handler repeating what it receives.
func echoHandler(ws *websocket.Conn) {
	path := ws.Request().URL.Path
	var msg jsonMessage
	var err error
	for {
		err = websocket.JSON.Receive(ws, &msg)
		if err == io.EOF {
			return
		}
		if err != nil {
			panic(err)
		}
		msg.Response = path
		if err = websocket.JSON.Send(ws, msg); err != nil {
			panic(err)
		}
	}
}

// jsonMessage holds messages used for testing the WebSocket handlers.
type jsonMessage struct {
	Request  string
	Response string
}
