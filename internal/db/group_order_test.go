package db

import (
 "path/filepath"
 "testing"
 "time"

 "github.com/ketchupz1999/agents-home-public/internal/protocol"
)

func TestGroupMessagesPreserveInsertionOrderWithinOneSecond(t *testing.T) {
 d,err:=Open(filepath.Join(t.TempDir(),"messages.db"));if err!=nil {t.Fatal(err)};defer d.Close()
 at:=time.Now().UTC().Truncate(time.Second)
 for _,m:=range []protocol.Message{
  {ID:"z-root",GroupID:"g",From:"owner",Item:"test",CreatedAt:at,Body:"question"},
  {ID:"a-reply",GroupID:"g",From:"agent",Item:"test",CreatedAt:at,ReplyTo:"z-root",Body:"reply"},
 } {if err:=d.PutGroupMessage(m);err!=nil {t.Fatal(err)}}
 for _,read:=range []func()([]protocol.Message,error){
  func()([]protocol.Message,error){return d.ListGroupMessages("g",0)},
  func()([]protocol.Message,error){return d.ListGroupMessagesByItem("test",0)},
  func()([]protocol.Message,error){return d.ListGroupMessagesAll(0)},
 } {messages,err:=read();if err!=nil {t.Fatal(err)};if len(messages)!=2 || messages[0].ID!="z-root" {t.Error("reply precedes question in same second")}}
}
