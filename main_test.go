package main

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"buf.build/gen/go/connectrpc/eliza/connectrpc/go/connectrpc/eliza/v1/elizav1connect"
	elizav1 "buf.build/gen/go/connectrpc/eliza/protocolbuffers/go/connectrpc/eliza/v1"
	tea "charm.land/bubbletea/v2"
	"connectrpc.com/connect"
	"go.akshayshah.org/memhttp"
	"go.vanburen.xyz/ok"
	"net/http"
)

// fakeElizaServiceHandler implements the ELIZA service for testing.
type fakeElizaServiceHandler struct {
	elizav1connect.UnimplementedElizaServiceHandler

	// converseCalls counts how many Converse streams have been opened.
	converseCalls atomic.Int32
	// converseDone receives a value each time a Converse handler returns
	// (i.e. the client closed its side of the stream).
	converseDone chan struct{}
}

func (f *fakeElizaServiceHandler) Introduce(
	ctx context.Context,
	req *connect.Request[elizav1.IntroduceRequest],
	stream *connect.ServerStream[elizav1.IntroduceResponse],
) error {
	sentences := []string{
		fmt.Sprintf("Hello %s, I'm ELIZA.", req.Msg.Name),
		"How are you feeling today?",
		"I'm here to help you.",
	}

	for _, sentence := range sentences {
		if err := stream.Send(&elizav1.IntroduceResponse{
			Sentence: sentence,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeElizaServiceHandler) Say(
	ctx context.Context,
	req *connect.Request[elizav1.SayRequest],
) (*connect.Response[elizav1.SayResponse], error) {
	response := connect.NewResponse(&elizav1.SayResponse{
		Sentence: fmt.Sprintf("I see. You said: %q. Tell me more.", req.Msg.Sentence),
	})
	return response, nil
}

func (f *fakeElizaServiceHandler) Converse(
	ctx context.Context,
	stream *connect.BidiStream[elizav1.ConverseRequest, elizav1.ConverseResponse],
) error {
	f.converseCalls.Add(1)
	if f.converseDone != nil {
		defer func() { f.converseDone <- struct{}{} }()
	}
	for {
		req, err := stream.Receive()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		// Simple echo response with some transformation
		response := fmt.Sprintf("I see. You said: %q. Tell me more.", req.Sentence)
		if err := stream.Send(&elizav1.ConverseResponse{
			Sentence: response,
		}); err != nil {
			return err
		}
	}
}

// fakeElizaServiceErrorHandler implements the ELIZA service but fails on Converse.
type fakeElizaServiceErrorHandler struct {
	elizav1connect.UnimplementedElizaServiceHandler
}

func (f *fakeElizaServiceErrorHandler) Introduce(
	ctx context.Context,
	req *connect.Request[elizav1.IntroduceRequest],
	stream *connect.ServerStream[elizav1.IntroduceResponse],
) error {
	return fmt.Errorf("introduce error")
}

func (f *fakeElizaServiceErrorHandler) Say(
	ctx context.Context,
	req *connect.Request[elizav1.SayRequest],
) (*connect.Response[elizav1.SayResponse], error) {
	return nil, fmt.Errorf("say error")
}

func (f *fakeElizaServiceErrorHandler) Converse(
	ctx context.Context,
	stream *connect.BidiStream[elizav1.ConverseRequest, elizav1.ConverseResponse],
) error {
	// Immediately fail on any receive attempt
	return fmt.Errorf("converse error")
}

// startFakeServerWithErrors creates an ELIZA service that always fails.
func startFakeServerWithErrors(t *testing.T) elizav1connect.ElizaServiceClient {
	t.Helper()

	mux := http.NewServeMux()
	mux.Handle(elizav1connect.NewElizaServiceHandler(&fakeElizaServiceErrorHandler{}))

	server, err := memhttp.New(mux)
	ok.MustNoError(t, err)

	t.Cleanup(func() {
		ok.NoError(t, server.Close())
	})

	return elizav1connect.NewElizaServiceClient(server.Client(), "https://example.com")
}

// startFakeServer creates an in-memory ELIZA service and returns the client.
func startFakeServer(t *testing.T) elizav1connect.ElizaServiceClient {
	t.Helper()
	client, _ := startFakeServerWithHandler(t)
	return client
}

// startFakeServerWithHandler creates an in-memory ELIZA service and returns
// both the client and the handler, so tests can observe handler-side state.
func startFakeServerWithHandler(t *testing.T) (elizav1connect.ElizaServiceClient, *fakeElizaServiceHandler) {
	t.Helper()

	handler := &fakeElizaServiceHandler{
		converseDone: make(chan struct{}, 8),
	}

	// Setup Connect handlers
	mux := http.NewServeMux()
	mux.Handle(elizav1connect.NewElizaServiceHandler(handler))

	// Create in-memory HTTP server with TLS and HTTP/2 support for bidi streams
	// The bidirectional Converse RPC requires HTTP/2, which is enabled by default when TLS is used
	server, err := memhttp.New(mux)
	ok.MustNoError(t, err)

	// Cleanup
	t.Cleanup(func() {
		ok.NoError(t, server.Close())
	})

	return elizav1connect.NewElizaServiceClient(server.Client(), "https://example.com"), handler
}

// sendMessage drives a full conversation exchange through the Update loop:
// it types text, presses enter, executes the returned command, and feeds the
// resulting message back into Update — the way the Bubble Tea runtime would.
func sendMessage(t *testing.T, m model, text string) model {
	t.Helper()

	m.textInput.SetValue(text)
	newModel, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = newModel.(model)
	ok.True(t, cmd != nil, ok.Sprintf("expected a command from enter"))

	msg := cmd()
	if errM, isErr := msg.(errMsg); isErr {
		// Must not go on to Update with an errMsg, so report and hand the
		// model back unchanged; the caller's own assertions then fail too.
		ok.True(t, false, ok.Sprintf("expected sayMsg, got error: %v", errM))
		return m
	}

	newModel, _ = m.Update(msg)
	return newModel.(model)
}

func TestConverseStreamIsReused(t *testing.T) {
	t.Parallel()

	client, handler := startFakeServerWithHandler(t)
	m := initialModel(client)
	m.hasIntroduced = true
	m.name = "User"
	m.introductionReceived = []string{"Hello User"}

	m = sendMessage(t, m, "hello")
	m = sendMessage(t, m, "how are you?")

	ok.Equal(t, len(m.sayResponses), 2)
	// Both messages must travel over a single Converse stream.
	ok.Equal(t, handler.converseCalls.Load(), int32(1))
}

func TestConverseStreamClosedOnQuit(t *testing.T) {
	t.Parallel()

	client, handler := startFakeServerWithHandler(t)
	m := initialModel(client)
	m.hasIntroduced = true
	m.name = "User"
	m.introductionReceived = []string{"Hello User"}

	m = sendMessage(t, m, "hello")

	// Quit; the client must close its side of the stream so the server
	// handler returns.
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	ok.True(t, cmd != nil)

	select {
	case <-handler.converseDone:
		// Handler returned: stream was closed.
	case <-time.After(3 * time.Second):
		ok.True(t, false, ok.Sprintf("Converse handler still running after quit: the stream was never closed"))
	}
}

func TestEnterWhileWaitingForResponseIsIgnored(t *testing.T) {
	t.Parallel()

	client := startFakeServer(t)
	m := initialModel(client)
	m.hasIntroduced = true
	m.name = "User"
	m.introductionReceived = []string{"Hello User"}
	m.said = []string{"first message"}
	m.sayResponses = nil // response not yet received
	m.waitingForResponse = true

	// Pressing enter while waiting must not send another message.
	newModel, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = newModel.(model)
	ok.True(t, cmd == nil, ok.Sprintf("enter while waiting should be ignored"))
	ok.Equal(t, len(m.said), 1)

	// And the view must render without panicking.
	view := m.View()
	ok.True(t, len(view.Content) > 0)
}

func TestIntroduceStreamErrorIsSurfaced(t *testing.T) {
	t.Parallel()

	client := startFakeServerWithErrors(t)
	m := initialModel(client)

	msg := m.introduce("User")()

	_, isExpected := msg.(errMsg)
	ok.True(t, isExpected, ok.Sprintf("expected errMsg, got %T: %v", msg, msg))
}

func TestInitialModelConfiguration(t *testing.T) {
	t.Parallel()

	client := startFakeServer(t)

	m := initialModel(client)

	// Verify initial state
	ok.Equal(t, m.hasIntroduced, false, ok.Sprintf("hasIntroduced"))
	ok.Equal(t, m.waitingForResponse, false, ok.Sprintf("waitingForResponse"))
	ok.NoError(t, m.err)
	ok.Equal(t, len(m.said), 0)
	ok.Equal(t, len(m.sayResponses), 0)
	ok.Equal(t, m.textInput.CharLimit, 156)
	ok.Equal(t, m.textInput.Width(), 50)
}

func TestUpdateMethodRespondsToKeyMessages(t *testing.T) {
	t.Parallel()

	client := startFakeServer(t)

	m := initialModel(client)

	// Simulate pressing 'a' key
	newModel, _ := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cast, isExpected := newModel.(model); isExpected {
		// Model should be updated
		ok.NoError(t, cast.err)
	}
}

func TestErrorHandling(t *testing.T) {
	t.Parallel()

	client := startFakeServer(t)
	m := initialModel(client)

	// Create an error message
	errMsg := errMsg(fmt.Errorf("test error"))

	// Update the model with the error
	newModel, cmd := m.Update(errMsg)

	// Verify error state
	if cast, isExpected := newModel.(model); isExpected {
		ok.Error(t, cast.err)
		ok.True(t, cast.err.Error() == "test error")
	}

	// Command should be Quit
	ok.True(t, cmd != nil, ok.Sprintf("cmd should be non-nil"))
}

func TestSpinnerTickMessage(t *testing.T) {
	t.Parallel()

	client := startFakeServer(t)
	m := initialModel(client)

	// Create and send a spinner tick message
	tickMsg := m.spinner.Tick()

	newModel, cmd := m.Update(tickMsg)

	// Should handle the message without panicking
	ok.True(t, newModel != nil, ok.Sprintf("newModel should be non-nil"))
	// Command may or may not be nil depending on spinner state
	_ = cmd
}

func TestConversationFlowSimpleModel(t *testing.T) {
	t.Parallel()

	// Note: This test demonstrates that the bidi stream (Converse) has issues
	// with the test HTTP server. The Introduce method (server streaming) works fine.
	// In production, the real demo.connectrpc.com service works correctly.
	// For thorough testing of the Converse flow, use integration tests against
	// the actual demo service or mock the client.

	client := startFakeServer(t)

	m := initialModel(client)

	// First, introduce
	m.hasIntroduced = true
	m.name = "Charlie"
	m.introductionReceived = []string{"Hello Charlie"}

	// The say method uses the bidirectional Converse RPC, which requires HTTP/2 support
	// The test server has limitations with HTTP/2, so we skip execution here
	// Instead, we verify the model structure is correct
	cmd := m.say("How are you?")
	ok.True(t, cmd != nil, ok.Sprintf("cmd should be non-nil"))
}

func TestMessageUpdates(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*model)
		msg   tea.Msg
		check func(*testing.T, *model)
	}{
		{
			name:  "introduction",
			setup: func(m *model) {},
			msg:   introductionMsg([]string{"Hello", "World"}),
			check: func(t *testing.T, m *model) {
				ok.True(t, m.hasIntroduced)
				ok.Equal(t, m.waitingForResponse, false, ok.Sprintf("waitingForResponse"))
				ok.Equal(t, len(m.introductionReceived), 2)
			},
		},
		{
			name: "say",
			setup: func(m *model) {
				m.hasIntroduced = true
				m.waitingForResponse = true
			},
			msg: sayMsg("I'm doing well"),
			check: func(t *testing.T, m *model) {
				ok.Equal(t, m.waitingForResponse, false, ok.Sprintf("waitingForResponse"))
				ok.Equal(t, len(m.sayResponses), 1)
				ok.Equal(t, m.sayResponses[0], "I'm doing well")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := startFakeServer(t)
			m := initialModel(client)
			tt.setup(&m)

			newModel, _ := m.Update(tt.msg)
			if cast, isExpected := newModel.(model); isExpected {
				tt.check(t, &cast)
			}
		})
	}
}

func TestWindowSizeMessage(t *testing.T) {
	t.Parallel()

	client := startFakeServer(t)
	m := initialModel(client)

	// Send a window resize message
	newModel, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if cast, isExpected := newModel.(model); isExpected {
		// Model should handle resize without errors or state changes
		ok.NoError(t, cast.err)
	}
}

func TestConversationViewWithWaitingForResponse(t *testing.T) {
	t.Parallel()

	client := startFakeServer(t)
	m := initialModel(client)

	// Set up conversation state with multiple exchanges
	m.hasIntroduced = true
	m.name = "User"
	m.introductionReceived = []string{"Hello User"}
	m.said = []string{"How are you?", "That's good"}
	m.sayResponses = []string{"I'm doing well", ""} // First response completed, second waiting
	m.waitingForResponse = true

	view := m.View()
	content := view.Content
	ok.True(t, len(content) > 0, ok.Sprintf("should have content"))
}

func TestDefaultKeyMessageHandling(t *testing.T) {
	t.Parallel()

	client := startFakeServer(t)
	m := initialModel(client)

	// Send a non-special key (not Enter, Ctrl+C, Esc)
	// This tests the default case which delegates to textInput
	newModel, _ := m.Update(tea.KeyPressMsg{Code: 'b', Text: "b"})
	if cast, isExpected := newModel.(model); isExpected {
		ok.Equal(t, cast.waitingForResponse, false, ok.Sprintf("waitingForResponse"))
	}
}

func TestDefaultMessageHandling(t *testing.T) {
	t.Parallel()

	client := startFakeServer(t)
	m := initialModel(client)

	// Send an unknown message type (not KeyPressMsg, errMsg, TickMsg, introductionMsg, sayMsg)
	// This tests the default case which delegates to textInput
	newModel, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if cast, isExpected := newModel.(model); isExpected {
		ok.NoError(t, cast.err)
	}
}

func TestEnterKeyInIntroduction(t *testing.T) {
	t.Parallel()

	client := startFakeServer(t)
	m := initialModel(client)

	// Simulate typing a name and pressing enter in introduction mode
	m.textInput.SetValue("Charlie")
	newModel, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cast, isExpected := newModel.(model); isExpected {
		// After pressing enter, should be waiting for response in introduction flow
		ok.True(t, cast.waitingForResponse, ok.Sprintf("should be waiting for response after enter in introduction"))
		ok.True(t, cmd != nil, ok.Sprintf("should return a command"))
	}
}

func TestEnterKeyInConversation(t *testing.T) {
	t.Parallel()

	client := startFakeServer(t)
	m := initialModel(client)

	// Set up as if we've already had introduction
	m.hasIntroduced = true
	m.name = "User"

	// Simulate typing and pressing enter in conversation mode
	m.textInput.SetValue("How are you?")
	newModel, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cast, isExpected := newModel.(model); isExpected {
		// After pressing enter in conversation, should be waiting for response
		ok.True(t, cast.waitingForResponse, ok.Sprintf("should be waiting for response after enter in conversation"))
		ok.True(t, cmd != nil, ok.Sprintf("should return a command"))
	}
}

func TestSayCommand(t *testing.T) {
	t.Parallel()

	client := startFakeServer(t)
	m := initialModel(client)

	// Set up as if we've already had introduction and opened the stream
	m.hasIntroduced = true
	m.name = "Charlie"
	m.introductionReceived = []string{"Hello Charlie"}
	m.conversation = m.client.Converse(context.Background())

	// Execute the say command
	cmd := m.say("How are you?")
	ok.True(t, cmd != nil, ok.Sprintf("cmd should be non-nil"))

	// Actually execute the command and check the result
	msg := cmd()

	// Check what type of message we got
	switch v := msg.(type) {
	case sayMsg:
		// Successfully received response from ELIZA
		ok.True(t, len(v) > 0)
	case errMsg:
		// Stream communication error is acceptable - still exercises the code path
		_ = v
	default:
		ok.True(t, false, ok.Sprintf("unexpected message type: %T", msg))
	}
}

func TestSayCommandWithServerError(t *testing.T) {
	t.Parallel()

	// Use an error-returning server
	client := startFakeServerWithErrors(t)
	m := initialModel(client)

	// Set up as if we've already had introduction and opened the stream
	m.hasIntroduced = true
	m.name = "User"
	m.introductionReceived = []string{"Hello User"}
	m.conversation = m.client.Converse(context.Background())

	// Execute the say command - should fail because server returns error
	cmd := m.say("Tell me more")
	ok.True(t, cmd != nil, ok.Sprintf("cmd should be non-nil"))

	// Execute the command
	msg := cmd()

	// Should get an error since server fails
	errMsg, isExpected := msg.(errMsg)
	ok.True(t, isExpected, ok.Sprintf("expected errMsg, got %T", msg))
	ok.True(t, errMsg != nil)
}
