package main

import "unsafe"

func main() {}

//go:wasmimport gateway_host outgoing
func outgoing(ptr, size uint32)

//go:wasmimport gateway_host state_put
func statePut(kp, kn, vp, vn uint32) int32

var buffer [65536]byte
var emitted bool

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
	data := []byte(`{"event_type":"delivery.result","ok":true}`)
	outgoing(uint32(uintptr(unsafe.Pointer(&data[0]))), uint32(len(data)))
	return 0
}
