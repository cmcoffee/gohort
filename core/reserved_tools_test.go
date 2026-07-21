package core

import "testing"

func TestReservedToolNames(t *testing.T) {
	if IsReservedToolName("send_message") {
		t.Fatal("precondition: send_message not reserved before registration")
	}
	RegisterReservedToolName("send_message", "  list_chats  ")
	if !IsReservedToolName("send_message") {
		t.Error("send_message should be reserved after registration")
	}
	if !IsReservedToolName("list_chats") {
		t.Error("names should be trimmed on registration")
	}
	if IsReservedToolName("get_meme") {
		t.Error("an unregistered name must not be reserved")
	}
	if IsReservedToolName("") {
		t.Error("empty is never reserved")
	}
}
