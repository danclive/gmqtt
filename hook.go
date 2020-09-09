package mqtt

import (
	"net"

	"github.com/danclive/mqtt/packets"
)

type Hooks struct {
	OnAccept
	OnStop
	OnSubscribe
	OnSubscribed
	OnUnsubscribed
	OnMsgArrived
	OnConnect
	OnConnected
	OnSessionCreated
	OnSessionResumed
	OnSessionTerminated
	OnDeliver
	OnAcked
	OnClose
	OnMsgDropped
}

// OnAccept 会在新连接建立的时候调用，只在TCP server中有效。如果返回false，则会直接关闭连接
//
// OnAccept will be called after a new connection established in TCP server. If returns false, the connection will be close directly.
type OnAccept func(conn net.Conn) bool

// OnStop will be called on server.Stop()
type OnStop func()

/*
OnSubscribe 返回topic允许订阅的最高QoS等级

OnSubscribe returns the maximum available QoS for the topic:
 0x00 - Success - Maximum QoS 0
 0x01 - Success - Maximum QoS 1
 0x02 - Success - Maximum QoS 2
 0x80 - Failure
*/
type OnSubscribe func(client Client, topic packets.Topic) (qos uint8)

// OnSubscribed will be called after the topic subscribe successfully
type OnSubscribed func(client Client, topic packets.Topic)

// OnUnsubscribed will be called after the topic has been unsubscribed
type OnUnsubscribed func(client Client, topicName string)

// OnMsgArrived 返回接收到的publish报文是否允许转发，返回false则该报文不会被继续转发
//
// OnMsgArrived returns whether the publish packet will be delivered or not.
// If returns false, the packet will not be delivered to any clients.
type OnMsgArrived func(client Client, msg packets.Message) (valid bool)

// OnClose tcp连接关闭之后触发
//
// OnClose will be called after the tcp connection of the client has been closed
type OnClose func(client Client, err error)

// OnConnect 当合法的connect报文到达的时候触发，返回connack中响应码
//
// OnConnect will be called when a valid connect packet is received.
// It returns the code of the connack packet
type OnConnect func(client Client) (code uint8)

// OnConnected 当客户端成功连接后触发
//
// OnConnected will be called when a mqtt client connect successfully.
type OnConnected func(client Client)

// OnSessionCreated 新建session时触发
//
// OnSessionCreated will be called when session  created.
type OnSessionCreated func(client Client)

// OnSessionResumed 恢复session时触发
//
// OnSessionResumed will be called when session resumed.
type OnSessionResumed func(client Client)

type SessionTerminatedReason byte

const (
	NormalTermination SessionTerminatedReason = iota
	ConflictTermination
	ExpiredTermination
)

// OnSessionTerminated session 终止时触发
//
// OnSessionTerminated will be called when session terminated.
type OnSessionTerminated func(client Client, reason SessionTerminatedReason)

// OnDeliver 分发消息时触发
//
//  OnDeliver will be called when publishing a message to a client.
type OnDeliver func(client Client, msg packets.Message)

// OnAcked 当客户端对qos1或qos2返回确认的时候调用
//
// OnAcked  will be called when receiving the ack packet for a published qos1 or qos2 message.
type OnAcked func(client Client, msg packets.Message)

// OnMessageDropped 丢弃消息后触发
//
// OnMsgDropped will be called after the msg dropped
type OnMsgDropped func(client Client, msg packets.Message)
