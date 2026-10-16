// testpeer is an isolated cross-language protocol fixture; it never opens real wing state.
package main

import (
 "bufio"
 "context"
 "crypto/ecdh"
 "crypto/rand"
 "encoding/json"
 "fmt"
 "net"
 "net/http"
 "os"
 "time"
 "github.com/ehrlich-b/wingthing/internal/phonepair"
)
type fixture struct{}
func(fixture)Execute(_ context.Context,_ string,op,_ string,input string)(any,error){switch op{case "sessions":return []map[string]any{{"id":"fixture-shell","name":"Disposable shell","agent":"shell","status":"idle","blocked":true,"unread":true}},nil;case "read":return map[string]any{"output":"fixture output\r\n","ended":false},nil;case "input":return map[string]any{"ok":input=="hello\r"},nil;default:return map[string]bool{"ok":true},nil}}
func(fixture)Close(string){}
func main(){
 dir,e:=os.MkdirTemp("","wingthing-pair-test-*");if e!=nil{panic(e)};defer os.RemoveAll(dir)
 private,_:=ecdh.X25519().GenerateKey(rand.Reader);ln,e:=net.Listen("tcp4","127.0.0.1:0");if e!=nil{panic(e)}
 manager,e:=phonepair.New(dir,"fixture-main-wing","my-mac","http://"+ln.Addr().String(),nil,private,fixture{});if e!=nil{panic(e)};defer manager.Close()
 q,e:=manager.Create();if e!=nil{panic(e)}
 mux:=http.NewServeMux();mux.HandleFunc("POST /phone",func(w http.ResponseWriter,r *http.Request){r.Body=http.MaxBytesReader(w,r.Body,phonepair.MaxPayload+4096);var req phonepair.Request;if json.NewDecoder(r.Body).Decode(&req)!=nil{http.Error(w,"bad request",400);return};w.Header().Set("Content-Type","application/json");_ = json.NewEncoder(w).Encode(manager.Box(r.Context(),req))})
 server:=&http.Server{Handler:mux,ReadHeaderTimeout:5*time.Second};go server.Serve(ln);defer server.Close()
 _ = json.NewEncoder(os.Stdout).Encode(map[string]string{"link":q.Link()})
 scanner:=bufio.NewScanner(os.Stdin);for scanner.Scan(){var err error;switch scanner.Text(){case "allow":err=manager.Allow(q.ID);case "revoke":err=manager.Revoke("My iPhone");case "stop":return;default:err=fmt.Errorf("unknown fixture control")};if err!=nil{_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"error":err.Error()})}else{_ = json.NewEncoder(os.Stdout).Encode(map[string]bool{"ok":true})}}
}
