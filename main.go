/*
Eliza interacts with the [Connect ELIZA demo service].

Usage:

	eliza

[Connect ELIZA demo service]: https://connectrpc.com/demo/
*/
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"buf.build/gen/go/connectrpc/eliza/connectrpc/go/connectrpc/eliza/v1/elizav1connect"
	elizav1 "buf.build/gen/go/connectrpc/eliza/protocolbuffers/go/connectrpc/eliza/v1"
	"connectrpc.com/connect"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	p := tea.NewProgram(
		initialModel(
			elizav1connect.NewElizaServiceClient(
				http.DefaultClient,
				"https://demo.connectrpc.com",
			),
		),
	)

	if _, err := p.Run(); err != nil {
		fmt.Printf("error: %s\n", err)
		os.Exit(1)
	}
}

type introductionMsg []string
type sayMsg string
type errMsg error

type model struct {
	client elizav1connect.ElizaServiceClient

	hasIntroduced      bool
	waitingForResponse bool

	conversation *connect.BidiStreamForClient[elizav1.ConverseRequest, elizav1.ConverseResponse]

	name                 string
	introductionReceived []string
	said                 []string
	sayResponses         []string

	textInput textinput.Model
	spinner   spinner.Model

	err error
}

func initialModel(client elizav1connect.ElizaServiceClient) model {
	textInput := textinput.New()
	textInput.Placeholder = "Joseph Weizenbaum"
	textInput.CharLimit = 156
	textInput.Width = 50
	textInput.Focus()

	return model{
		client:    client,
		textInput: textInput,
		spinner:   spinner.New(),
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.spinner.Tick)
}

func (m model) introduce(name string) tea.Cmd {
	return func() tea.Msg {
		introduceResponse, err := m.client.Introduce(context.Background(),
			connect.NewRequest(&elizav1.IntroduceRequest{
				Name: name,
			}),
		)
		if err != nil {
			return errMsg(err)
		}
		introductionLines := []string{}
		for introduceResponse.Receive() {
			introductionLines = append(introductionLines, introduceResponse.Msg().Sentence)
		}
		return introductionMsg(introductionLines)
	}
}

func (m model) say(text string) tea.Cmd {
	return func() tea.Msg {
		if m.conversation == nil {
			m.conversation = m.client.Converse(context.Background())
		}
		if err := m.conversation.Send(
			&elizav1.ConverseRequest{
				Sentence: text,
			},
		); err != nil {
			return errMsg(err)
		}
		conversationResponse, err := m.conversation.Receive()
		if err != nil {
			return errMsg(err)
		}
		// Eliza is too fast to respond, generally.
		// Wait a second to make things appear slow.
		time.Sleep(time.Second)
		return sayMsg(conversationResponse.Sentence)
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEnter:
			m.waitingForResponse = true
			text := m.textInput.Value()
			m.textInput.Reset()
			if !m.hasIntroduced {
				m.name = text
				m.textInput.Placeholder = ""
				return m, m.introduce(text)
			} else {
				m.said = append(m.said, text)
				return m, m.say(text)
			}
		case tea.KeyCtrlC, tea.KeyEsc:
			return m, tea.Quit
		default:
			m.textInput, cmd = m.textInput.Update(msg)
			return m, cmd
		}
	case errMsg:
		m.err = msg
		return m, tea.Quit
	case spinner.TickMsg:
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case introductionMsg:
		m.hasIntroduced = true
		m.waitingForResponse = false
		m.introductionReceived = msg
		return m, nil
	case sayMsg:
		m.waitingForResponse = false
		m.sayResponses = append(m.sayResponses, string(msg))
		return m, nil
	default:
		m.textInput, cmd = m.textInput.Update(msg)
		return m, cmd
	}
}

func (m model) View() string {
	if m.err != nil {
		return fmt.Sprintf("An error occurred: %s", m.err)
	}
	if !m.hasIntroduced {
		return m.introductionView()
	}
	return m.conversationView()
}

func (m model) introductionView() string {
	var introduction strings.Builder
	introduction.WriteString("Let's introduce you! - what's your name?")
	introduction.WriteString("\n")
	introduction.WriteString("\n")
	if m.waitingForResponse {
		introduction.WriteString(m.spinner.View())
	} else {
		introduction.WriteString(m.textInput.View())
	}
	return introduction.String()
}

func (m model) conversationView() string {
	var conversation strings.Builder
	// Write introduction
	for _, introductionLine := range m.introductionReceived {
		conversation.WriteString("Eliza: ")
		conversation.WriteString(introductionLine)
		conversation.WriteString("\n")
	}
	conversation.WriteString("\n")
	// Write conversation
	for i := 0; i < len(m.said); i++ {
		// Things we've said
		conversation.WriteString(m.name)
		conversation.WriteString(": ")
		conversation.WriteString(m.said[i])
		conversation.WriteString("\n")
		// Things Eliza has said
		conversation.WriteString("Eliza: ")
		// If this is the last thing Eliza has said and we're waiting for a
		// response, show the spinner.
		// Otherwise, show the response.
		if i == len(m.said)-1 && m.waitingForResponse {
			conversation.WriteString(m.spinner.View())
		} else {
			conversation.WriteString(m.sayResponses[i])
		}
		conversation.WriteString("\n")
	}
	if !m.waitingForResponse {
		conversation.WriteString(m.textInput.View())
	}
	return conversation.String()
}
