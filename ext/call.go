package ext

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/danclive/mqtt"
	"github.com/danclive/mqtt/packets"
	"github.com/danclive/nson-go"
	"go.uber.org/zap"
)

// 请求
const (
	CALL_ID = "call_id"
	CALL    = "call"
	LLAC    = "llac"
)

// 请求相关
const (
	PARAMS    = "params"
	CODE      = "code"
	ERROR     = "error"
	DATA      = "data"
	DATA_SIZE = "data_size"
)

type MqttCall struct {
	server  mqtt.Server
	calling map[string]*CallToken
	llacs   map[string]func(nson.Message) nson.Message
	lock    sync.Mutex
	suger   *zap.SugaredLogger
}

func NewMqttCall(zaplog *zap.Logger) *MqttCall {
	call := &MqttCall{
		calling: make(map[string]*CallToken),
		llacs:   make(map[string]func(nson.Message) nson.Message),
		suger:   zaplog.Sugar(),
	}

	call.Llac("ping", func(params nson.Message) nson.Message {
		return params
	})

	return call
}

var _ mqtt.Plugin = &MqttCall{}

func (m *MqttCall) Name() string {
	return "mqtt_call"
}

func (m *MqttCall) Load(server mqtt.Server) error {
	m.server = server
	return nil
}

func (m *MqttCall) Unload() error {
	return nil
}

func (m *MqttCall) Hooks() mqtt.Hooks {
	return mqtt.Hooks{
		OnMsgArrived: m.msgArrived,
	}
}

func (m *MqttCall) msgArrived(client mqtt.Client, msg packets.Message) (valid bool) {
	if msg.Topic() == LLAC {
		reader := bytes.NewBuffer(msg.Payload())
		nsonValue, err := nson.Message{}.Decode(reader)
		if err != nil {
			m.suger.Errorf("nson.Message{}.Decode(reader): %s", err)
			return
		}

		message, ok := nsonValue.(nson.Message)
		if !ok {
			return
		}

		callId, err := message.GetString(CALL_ID)
		if err != nil {
			return
		}

		data, err := message.GetMessage(DATA)
		if err != nil {
			return
		}

		m.lock.Lock()
		callToken, ok := m.calling[callId]
		if ok {
			delete(m.calling, callId)
		}
		m.lock.Unlock()

		if !ok {
			return
		}

		callToken.setMessage(data)

		return
	} else if strings.HasPrefix(msg.Topic(), CALL) {
		reader := bytes.NewBuffer(msg.Payload())
		nsonValue, err := nson.Message{}.Decode(reader)
		if err != nil {
			m.suger.Errorf("nson.Message{}.Decode(reader): %s", err)
			return
		}

		message, ok := nsonValue.(nson.Message)
		if !ok {
			return
		}

		callId, err := message.GetString(CALL_ID)
		if err != nil {
			return
		}

		data, err := message.GetMessage(DATA)
		if err != nil {
			return
		}

		m.lock.Lock()
		fn, ok := m.llacs[msg.Topic()]
		m.lock.Unlock()
		if !ok {
			return
		}

		go func() {
			ret := fn(data)

			msg := nson.Message{
				DATA:    ret,
				CALL_ID: nson.String(callId),
			}

			buffer := new(bytes.Buffer)
			err := msg.Encode(buffer)
			if err != nil {
				m.suger.Errorf("msg.Encode(buffer): %s", err)
				return
			}

			m.server.PublishService().PublishToClient(client.OptionsReader().ClientID(), mqtt.NewMessage(LLAC, buffer.Bytes(), 0), false)
		}()

		return
	}

	return true
}

func (m *MqttCall) Call(clientID, ch string, data nson.Message, timeout time.Duration) (nson.Message, error) {
	if ch == "" {
		return nil, errors.New("消息频道不能为空")
	}

	callId := mqtt.RandomID()

	msg := nson.Message{
		DATA:    data,
		CALL_ID: nson.String(callId),
	}

	token := newCallToken()
	m.lock.Lock()
	m.calling[callId] = token
	m.lock.Unlock()

	defer func() {
		m.lock.Lock()
		if token, ok := m.calling[callId]; ok {
			token.flowComplete()
			delete(m.calling, callId)
		}
		m.lock.Unlock()
	}()

	buffer := new(bytes.Buffer)
	err := msg.Encode(buffer)
	if err != nil {
		return nil, err
	}

	m.server.PublishService().PublishToClient(clientID, mqtt.NewMessage(CALL+"."+ch, buffer.Bytes(), 0), false)

	if !token.WaitTimeout(timeout) {
		return nil, errors.New("timeout")
	}

	err = token.Error()
	if err != nil {
		return nil, err
	}

	return token.Message(), nil
}

func (m *MqttCall) Llac(ch string, callback func(nson.Message) nson.Message) error {
	if ch == "" {
		return errors.New("消息频道不能为空")
	}

	m.lock.Lock()
	defer m.lock.Unlock()

	m.llacs[CALL+"."+ch] = callback

	return nil
}
