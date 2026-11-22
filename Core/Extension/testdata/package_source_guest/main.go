package main

import (
	"bytes"
	"unsafe"
)

//go:wasm-module extension_host
//export outgoing
func hostOutgoing(pointer, size uint32)

var eventBuffer [65536]byte

//export package_source_abi_version
func abiVersion() int32 { return 1 }

//export package_source_event_buf
func bufferPointer() uint32 { return uint32(uintptr(unsafe.Pointer(&eventBuffer[0]))) }

//export package_source_event_buf_size
func bufferSize() uint32 { return uint32(len(eventBuffer)) }

//export package_source_init
func initialize() int32 { return 0 }

func emit(value string) {
	data := []byte(value)
	hostOutgoing(uint32(uintptr(unsafe.Pointer(&data[0]))), uint32(len(data)))
}

//export package_source_handle_event
func handleEvent(_ uint32, size uint32) int32 {
	event := eventBuffer[:size]
	switch {
	case bytes.Contains(event, []byte("package_source.search")):
		emit(`{"event_type":"package_source.search.result","request_id":"wasm-source:1","result":{"packages":[{"reference":{"id":"wasm-skill","version":"1"},"kind":"skill","name":"WASM Skill","description":"Provided by an independent guest"}]}}`)
	case bytes.Contains(event, []byte("package_source.plan")):
		emit(`{"event_type":"package_source.plan.result","request_id":"wasm-source:2","result":{"version":1,"id":"wasm-plan","source_id":"guest","operation":"install","created_at":"2026-01-01T00:00:00Z","approval":{"state":"ready"},"items":[{"package":{"source_id":"guest","id":"wasm-skill","version":"1"},"kind":"skill","artifact":{"id":"artifact","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":3,"media_type":"application/zip"}}]}}`)
	case bytes.Contains(event, []byte("package_source.acquire")):
		emit(`{"event_type":"package_source.artifact.chunk","request_id":"wasm-source:3","data":"YWJj"}`)
		emit(`{"event_type":"package_source.artifact.complete","request_id":"wasm-source:3"}`)
	}
	return 0
}

//export package_source_poll
func poll() int32 {
	emit(`{"event_type":"package_source.proposal","result":{"id":"proposal-1","reason":"guest update","created_at":"2026-01-01T00:00:00Z","plan":{"version":1,"id":"proposal-plan","source_id":"guest","operation":"update","created_at":"2026-01-01T00:00:00Z","approval":{"state":"ready"},"items":[{"package":{"source_id":"guest","id":"wasm-skill","version":"1"},"kind":"skill","artifact":{"id":"artifact","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":3,"media_type":"application/zip"}}]}}}`)
	return 0
}

//export package_source_shutdown
func shutdown() int32 { return 0 }

func main() {}
