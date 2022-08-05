package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bufbuild/connect-go"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	elizav1 "go.buf.build/bufbuild/connect-go/bufbuild/eliza/buf/connect/demo/eliza/v1"
	"go.buf.build/bufbuild/connect-go/bufbuild/eliza/buf/connect/demo/eliza/v1/elizav1connect"
)

func main() {
	p := tea.NewProgram(
		initialModel(
			elizav1connect.NewElizaServiceClient(
				http.DefaultClient,
				"https://demo.connect.build",
			),
		),
	)

	if err := p.Start(); err != nil {
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

	introductionReceived []string
	said                 []string
	sayResponses         []string

	textInput textinput.Model
	spinner   spinner.Model

	err error
}

func initialModel(client elizav1connect.ElizaServiceClient) model {
	ti := textinput.New()
	ti.Placeholder = "Joseph Weizenbaum"
	ti.Focus()
	ti.CharLimit = 156
	ti.Width = 50

	return model{
		client:    client,
		textInput: ti,
		spinner:   spinner.New(),
		err:       nil,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, spinner.Tick)
}

func (m model) introduce(text string) tea.Cmd {
	return func() tea.Msg {
		introResp, err := m.client.Introduce(context.Background(),
			connect.NewRequest(&elizav1.IntroduceRequest{
				Name: text,
			}),
		)
		if err != nil {
			return errMsg(err)
		}
		introductionLines := []string{}
		for introResp.Receive() {
			introductionLines = append(introductionLines, introResp.Msg().Sentence)
		}
		return introductionMsg(introductionLines)
	}
}

func (m model) say(text string) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.Say(context.Background(),
			connect.NewRequest(&elizav1.SayRequest{
				Sentence: text,
			}),
		)
		time.Sleep(time.Second)
		if err != nil {
			return errMsg(err)
		}
		return sayMsg(resp.Msg.Sentence)
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
		return m, nil
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
	if !m.hasIntroduced {
		return m.introductionView()
	}
	return m.conversationView()
}

func (m model) introductionView() string {
	var s strings.Builder
	s.WriteString("Let's introduce you! - what's your name?")
	s.WriteString("\n")
	s.WriteString("\n")
	if m.waitingForResponse {
		s.WriteString(m.spinner.View())
	} else {
		s.WriteString(m.textInput.View())
	}
	return s.String()
}

func (m model) conversationView() string {
	var s strings.Builder
	// Write introduction
	for _, introductionLine := range m.introductionReceived {
		s.WriteString(introductionLine)
		s.WriteString("\n")
	}
	s.WriteString("\n")
	// Write conversation
	for i := 0; i < len(m.said); i++ {
		// Things we've said
		s.WriteString("You: ")
		s.WriteString(m.said[i])
		s.WriteString("\n")
		// Things Eliza has said
		s.WriteString("Eliza: ")
		// If this is the last thing Eliza has said and we're waiting for a
		// response, show the spinner.
		// Otherwise, show the response.
		if i == len(m.said)-1 && m.waitingForResponse {
			s.WriteString(m.spinner.View())
		} else {
			s.WriteString(m.sayResponses[i])
		}
		s.WriteString("\n")
	}
	if !m.waitingForResponse {
		s.WriteString(m.textInput.View())
	}
	return s.String()
}
