package onebot

import "time"

// LifecycleEvent builds a lifecycle meta event.
func LifecycleEvent(selfID any, subType string) *Event {
	return lifecycleEvent(selfID, subType)
}

// HeartbeatEvent builds a heartbeat meta event.
func HeartbeatEvent(selfID any, intervalMS int64, online bool) *Event {
	return heartbeatEvent(selfID, intervalMS, online)
}

// lifecycleEvent builds a lifecycle meta event.
func lifecycleEvent(selfID any, subType string) *Event {
	return &Event{
		Time:          time.Now().Unix(),
		SelfID:        selfID,
		PostType:      "meta_event",
		MetaEventType: "lifecycle",
		SubType:       subType,
	}
}

// heartbeatEvent builds a heartbeat meta event.
func heartbeatEvent(selfID any, interval int64, online bool) *Event {
	return &Event{
		Time:          time.Now().Unix(),
		SelfID:        selfID,
		PostType:      "meta_event",
		MetaEventType: "heartbeat",
		Interval:      interval,
		Status:        &Status{Online: online, Good: online},
	}
}
