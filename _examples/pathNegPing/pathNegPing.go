// Copyright 2018 ETH Zurich
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
    "flag"
    "fmt"
    "time"
    "os"
    "math/rand"

    "github.com/pascabr/scion-apps/pkg/pathNeg"
    // "github.com/scionproto/scion/go/lib/snet"
    // "../../pkg/pathNeg/pathNeg"
    // "scion-apps/pkg/pathNeg"
)


var (
    serverPort uint16 = 1234
    // serverAddr string = "1-ff00:0:131,[127.0.0.77]:1234"
    // clientAddr string = "1-ff00:0:112,[127.0.0.60]"
    serverAddr string = "1-ff00:0:131,[127.0.0.1]:1234"
    clientAddr string = "1-ff00:0:112,[127.0.0.1]"
)
const sleepTime int = 3

func main(){
    var err error
    // get the type of host to run server or client
    server := flag.Bool("server",false,"Mark execution to be a server")
    client := flag.Bool("client", false, "Mark execution to be a client")

    if ( *server && *client ){
        fmt.Printf("Can't be both client and server")
        return
    }
    flag.Parse()
    // we have either server or client
    // start accordingly
    if (*server){
        err = run_server()
        check(err)
    } else if (*client){
        err = run_client()
        check(err)
    } else{
        fmt.Printf("No type selected")
        return
    }
}


func run_server() error {

    //initialize path neg conn
    pnc, err := pathNeg.NewPathNegConn()
    if err != nil {
        fmt.Printf("[Server] Error generating new PathNegConn!\n")
        return err
    }

    fmt.Printf("[Server] Start listening to port: %d\n",serverPort)
    // addr := &net.UDPAddr{Port: int(serverPort),IP: serverAddr}
    pnc.ListenPort(serverPort)

    buffer := make([]byte, 16*1024)
    for {

        fmt.Printf("[Server] Receiving....\n")
        // receive packet
        n,from,err := pnc.ReadFrom(buffer)
        if err != nil{
            fmt.Printf("[Server] Error Reading packet!\n")
            return nil
        }
        // print packet
        fmt.Printf("[Server] Packet: %s, size: %d\n",string(buffer),n)

        response := []byte("Hello Back")

        // print paths to sender
        fmt.Printf("[Server] Paths to Sender: \n")
        fromAddr,err := pathNeg.ResolveUDPAddr(from.String())
        if err != nil{
            return err
        }
        paths,err := pnc.GetPaths(fromAddr)
        if err == nil{
            for n,p := range paths{
                fmt.Printf("Path %d:", n)
                fmt.Println(p)
            }
        }

        // pick a path at random
        r := rand.Int() % len(paths)
        fmt.Printf("[Server] Selcted Path %d: %s\n", r,paths[r])
        path := paths[r].Path().Copy()
        // err = path.Reverse()
        // if err != nil{
        //     return err
        // }

        fmt.Printf("[Server] Path: %s\n",path)
        // from = snet.UDPAddr* (from)
        // from.Path = paths[r].Path()
        // from = net.Addr (from)
        n, err = pnc.SendPath(paths[r], from)
        if (err != nil){
            fmt.Printf("[Server] Error Sending Path\n")
            fmt.Printf("[Server] %s\n",err)
            return err
        }

        // fmt.Printf("[Server] Receiving....\n")
        // // receive packet
        // n,from,err = pnc.ReadFrom(buffer)
        // if err != nil{
        //     fmt.Printf("[Server] Error Reading packet!\n")
        //     return nil
        // }
        // // print packet
        // fmt.Printf("[Server] Packet: %s, size: %d\n",string(buffer),n)

        fmt.Printf("[Server] Sending to %s....\n",from)
        // send back --> same path
        n, err = pnc.WriteTo(response, from)
        if err != nil{
            fmt.Printf("[Server] Error Writing back!\n")
            return err
        }
        time.Sleep(3*time.Second)

    }



}

func run_client() error {

    //initialize path neg conn
    pnc, err := pathNeg.NewPathNegConn()
    if err != nil {
        fmt.Printf("[Client] Couldn't create Conn\n")
        return err
    }

    err = pnc.Dial(serverAddr)
    if err != nil {
        fmt.Printf("[Client] Couldn't Dial Addr \n")
        return err
    }

    fmt.Printf("[Client] Starting...\n")

    for{
        fmt.Printf("[Client] Sending ...\n")
        // send hello to server
        s := []byte("Hello World")
        nBytes, err := pnc.Write(s)
        if err != nil || nBytes != len(s){
            fmt.Printf("[Client] Couldn't send data\n")
            return err
        }

        fmt.Printf("[Client] Receiving ...\n")
        // receive hello back
        buffer := make([]byte, 1024*16)
        nBytes, err = pnc.Read(buffer)
        if err != nil {
            fmt.Printf("[Client] Coudn't Read\n")
            return err
        }

        // print returned string
        retString := string(buffer[0:nBytes])
        fmt.Printf("[Client] Received: %s\n", retString)

        // wait before sending again
        time.Sleep(3 * time.Second)

    }


}


func check(err error){
    if err != nil{
        fmt.Fprintln(os.Stderr, "Fatal error. Exiting.", "err",err)
        os.Exit(1)
    }
    os.Exit(0)
}
