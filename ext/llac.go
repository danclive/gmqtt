package ext

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"time"

	broker "github.com/danclive/mqtt"
	"github.com/danclive/nson-go"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"go.uber.org/zap"
)

type MqttLlac struct {
	client  mqtt.Client
	calling map[string]*CallToken
	llacs   map[string]func(nson.Message) nson.Message
	lock    sync.Mutex
	suger   *zap.SugaredLogger
}

func NewMqttLlac(client mqtt.Client, zaplog *zap.Logger) *MqttLlac {
	llac := &MqttLlac{
		client:  client,
		calling: make(map[string]*CallToken),
		llacs:   make(map[string]func(nson.Message) nson.Message),
		suger:   zaplog.Sugar(),
	}

	llac.client.Subscribe(LLAC, 0, llac.msgArrived)

	return llac
}

func (m *MqttLlac) msgArrived(client mqtt.Client, msg mqtt.Message) {
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

			client.Publish(LLAC, 0, false, buffer.Bytes())
		}()
	}
}

func (m *MqttLlac) Call(ch string, data nson.Message, timeout time.Duration) (nson.Message, error) {
	if ch == "" {
		return nil, errors.New("消息频道不能为空")
	}

	callId := broker.RandomID()

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

	m.client.Publish(CALL+"."+ch, 0, false, buffer.Bytes())

	if !token.WaitTimeout(timeout) {
		return nil, errors.New("timeout")
	}

	err = token.Error()
	if err != nil {
		return nil, err
	}

	return token.Message(), nil
}

func (m *MqttLlac) Llac(ch string, callback func(nson.Message) nson.Message) error {
	if ch == "" {
		return errors.New("消息频道不能为空")
	}

	m.lock.Lock()
	m.llacs[CALL+"."+ch] = callback
	m.lock.Unlock()

	token := m.client.Subscribe(CALL+"."+ch, 0, m.msgArrived)

	if token.WaitTimeout(time.Second*10) && token.Error() != nil {
		return token.Error()
	}

	return nil
}
