package main

import (
	"unsafe"
)

func main() {}

//go:wasmimport gateway_host outgoing
func outgoing(ptr, size uint32)

//go:wasmimport gateway_host state_put
func statePut(kp, kn, vp, vn uint32) int32

var buffer [65536]byte
var emitted bool
var deliveryID string

//export gateway_event_buf
func eventBuffer() uint32 { return uint32(uintptr(unsafe.Pointer(&buffer[0]))) }

//export gateway_event_buf_size
func eventBufferSize() uint32 { return uint32(len(buffer)) }

//export gateway_abi_version
func abiVersion() int32 { return 2 }

//export gateway_init
func initGateway() int32 { return 0 }

//export gateway_shutdown
func shutdownGateway() int32 { return 0 }

//export gateway_poll
func pollGateway() int32 {
	if emitted {
		return 0
	}
	emitted = true
	data := []byte(`{"event_version":1,"event_type":"message.incoming","source":"synthetic","update_id":1,"chat_id":7,"sender_id":9,"text":"hello"}`)
	outgoing(uint32(uintptr(unsafe.Pointer(&data[0]))), uint32(len(data)))
	key, value := []byte("probe"), []byte("denied")
	return statePut(uint32(uintptr(unsafe.Pointer(&key[0]))), uint32(len(key)), uint32(uintptr(unsafe.Pointer(&value[0]))), uint32(len(value)))
}

//export gateway_handle_event
func handleEvent(ptr, size uint32) int32 {
	data := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), size)
	valid := []byte(`{"event_type":"test.result","ok":true}`)
	if contains(data, `"type":"test.partial_invalid"`) {
		outgoing(uint32(uintptr(unsafe.Pointer(&valid[0]))), uint32(len(valid)))
		invalid := []byte(`{`)
		outgoing(uint32(uintptr(unsafe.Pointer(&invalid[0]))), uint32(len(invalid)))
		return 0
	}
	if contains(data, `"type":"test.partial_oversized"`) {
		outgoing(uint32(uintptr(unsafe.Pointer(&valid[0]))), uint32(len(valid)))
		outgoing(uint32(uintptr(unsafe.Pointer(&buffer[0]))), uint32(len(buffer)+1))
		return 0
	}
	if contains(data, `"type":"test.partial_excessive"`) {
		for j := 0; j < 65; j++ {
			outgoing(uint32(uintptr(unsafe.Pointer(&valid[0]))), uint32(len(valid)))
		}
		return 0
	}
	if contains(data, `"type":"test.success"`) {
		outgoing(uint32(uintptr(unsafe.Pointer(&valid[0]))), uint32(len(valid)))
		return 0
	}
	if contains(data, `"type":"session.route"`) {
		deliveryID = stringField(data, `"delivery_id":"`)
	}
	if contains(data, `"type":"session.event"`) {
		result := []byte(`{"event_type":"delivery.result","delivery_id":"` + deliveryID + `","ok":true}`)
		outgoing(uint32(uintptr(unsafe.Pointer(&result[0]))), uint32(len(result)))
	}
	return 0
}

func contains(data []byte, target string) bool { return find(data, target) >= 0 }

func stringField(data []byte, prefix string) string {
	start := find(data, prefix)
	if start < 0 {
		return ""
	}
	start += len(prefix)
	end := start
	for end < len(data) && data[end] != '"' {
		end++
	}
	return string(data[start:end])
}

func find(data []byte, target string) int {
	for i := 0; i+len(target) <= len(data); i++ {
		matched := true
		for j := range target {
			if data[i+j] != target[j] {
				matched = false
				break
			}
		}
		if matched {
			return i
		}
	}
	return -1
}
