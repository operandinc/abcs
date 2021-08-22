package imessage

// This file is used to differentiate between the different protocols available
// (e.g. iMessage, SMS), and to store the protocol in use for a particular chat.

type protocol int

const (
	imessage protocol = iota
	sms
)

func (p protocol) String() string {
	switch p {
	case imessage:
		return "iMessage"
	case sms:
		return "SMS"
	default:
		panic("unknown protocol")
	}
}

var protocolCache = make(map[string]protocol)

func recordChatProtocol(from string, proto protocol) {
	protocolCache[from] = proto
}

func getChatProtocol(to string) protocol {
	if v, ok := protocolCache[to]; ok {
		return v
	}
	return imessage
}
