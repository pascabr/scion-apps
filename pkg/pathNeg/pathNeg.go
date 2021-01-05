// Copyright 2020 ETH Zurich
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

/*
Package appnet provides a simplified and functionally extended wrapper interface to the
scionproto/scion package snet.


Dispatcher and SCION daemon connections

During the hidden initialisation of this package, the dispatcher and sciond
connections are opened. The sciond connection determines the local IA.
The dispatcher and sciond sockets are assumed to be at default locations, but this can
be overridden using environment variables:

		SCION_DISPATCHER_SOCKET: /run/shm/dispatcher/default.sock
		SCION_DAEMON_ADDRESS: 127.0.0.1:30255

This is convenient for the normal use case of running a the endhost stack for a
single SCION AS. When running multiple local ASes, e.g. during development, the
address of the sciond corresponding to the desired AS needs to be specified in
the SCION_DAEMON_ADDRESS environment variable.


Wildcard IP Addresses

snet does not currently support binding to wildcard addresses. This will hopefully be
added soon-ish, but in the meantime, this package emulates this functionality.
There is one restriction, that applies to hosts with multiple IP addresses in the AS:
the behaviour will be that of binding to one specific local IP address, which means that
the application will not be reachable using any of the other IP addresses.
Traffic sent will always appear to originate from this specific IP address,
even if that's not the correct route to a destination in the local AS.

This restriction will very likely not cause any issues, as a fairly contrived
network setup would be required. Also, sciond has a similar restriction (binds
to one specific IP address).
*/
package pathNeg

import (
    "bytes"
	"context"
    "encoding/gob"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/sciond"
	"github.com/scionproto/scion/go/lib/snet"
	"github.com/scionproto/scion/go/lib/serrors"
	"github.com/scionproto/scion/go/lib/snet/addrutil"
	"github.com/scionproto/scion/go/lib/sock/reliable"
    // "github.com/scionproto/scion/go/lib/slayers"
)

type PathNegConn struct{
   currConn *snet.Conn
   otherConns []*snet.Conn
   listenConn *snet.Conn
   otherListen []*snet.Conn
}

// Network extends the snet.Network interface by making the local IA and common
// sciond connections public.
// The default singleton instance of this type is obtained by the DefNetwork
// function.
type Network struct {
	snet.Network
	IA            addr.IA
	PathQuerier   snet.PathQuerier
	hostInLocalAS net.IP
}

const (
	initTimeout = 1 * time.Second
    newPathType byte = 2
    dataType byte = 1
)

var (
    errNoCloseConn = serrors.New("No connection to close available!")
    errNoReadConn = serrors.New("No connection to read from!")
    errNoReadFromConn = serrors.New("No listening connection to read from!")
    errInvalidType = serrors.New("Received invalid/unknown type")
    errFailedCopy = serrors.New("Failed to copy data to provided buffer")
    errNoWriteConn = serrors.New("No connection to write to!")
    errNoWriteToConn = serrors.New("No listending connection to write to!")
    errPathProb = serrors.New("No Path found!")
)

var dNet Network

func DefNetwork() *Network{
    return &dNet
}

func NewPathNegConn() (PathNegConn, error){
    var conn PathNegConn

    //initialize Network
    err := initDefNetwork()
    if err != nil{
        fmt.Printf("Error creating default net\n")
        return conn,err
    }

    conn.otherConns = make([]*snet.Conn,0)

    fmt.Printf("[Library] Created PathNegConn\n")

    return conn,nil
}

// dial address give by string of destination
func (p *PathNegConn) Dial(address string) error{
    // if we already have a connection
    // move the current one to the slice
    // use the new one as default if p.currConn != nil{
    p.otherConns = append(p.otherConns,p.currConn)

    raddr, err := ResolveUDPAddr(address)
    if err != nil{
        return err
    }
    fmt.Printf("[Library] Resolved Address: %s\n",raddr)

    c, err := dialAddr(raddr)
    if err != nil{
        return err
    }
    p.currConn = c

    return nil
}

// dial address directly
func (p *PathNegConn) DialAddr(addr *snet.UDPAddr) error{
    // if we already have a connection
    // move the current one to the slice
    // use the new one as default
    if p.currConn != nil{
        p.otherConns = append(p.otherConns,p.currConn)
    }

    c, err := dialAddr(addr)
    if err != nil{
        return err
    }
    p.currConn = c

    // start listening in a goroutine
    // go start_listening(c)

    return nil
}

// close current connection
func (p *PathNegConn) CloseCurr() error{
    // check if we have a connection
    if p.currConn == nil {
        return errNoCloseConn
    }

    // close connection
    err := p.currConn.Close()
    if err != nil {
        return err
    }

    // if there is another connection restore the last one
    l := len(p.otherConns)
    if l > 0 {
        p.currConn = p.otherConns[l-1]
        p.otherConns = p.otherConns[:l-1]
    }

    return nil
}

// close all connections
func (p *PathNegConn) CloseAll() error {
    // check if we have a connection
    if p.currConn == nil {
        return errNoCloseConn
    }

    // close current connection
    err := p.currConn.Close()
    if err != nil{
        return err
    }

    // close all other connections
    for _,c := range p.otherConns{
        err = c.Close()
        if err != nil{
            return err
        }
    }

    p.currConn = nil
    p.otherConns = make([]*snet.Conn,0)

    return nil
}

// Read from the current connection
func (p *PathNegConn) Read (buf []byte) (int,error){
    // check if we have a connection to listen from
    if p.currConn == nil{
        return 0,errNoReadConn
    }

    fmt.Printf("[Library] Starting to read...\n")

    localBuffer := make([]byte,len(buf)+1)
    n, err := p.currConn.Read(localBuffer)
    if err != nil{
        return 0,err
    }
    fmt.Printf("[Library] Got Data\n")

    // check for new path
    if localBuffer[0] == newPathType{
        fmt.Printf("[Library] Got new path...\n")
        // new path --> use it
        // read old destination
        remote := p.currConn.RemoteAddr()
        udpAddr := remote.String()
        fmt.Printf("[Library] Remote is: %s\n", udpAddr)

        // generate new snet.UDPAddr
        raddr, err := ResolveUDPAddr(udpAddr)
        if err != nil{
            return 0,err
        }
        fmt.Printf("[Library] Resolve dest to: %s\n", udpAddr)

        // decode path from network packet
        buffer := bytes.NewBuffer(localBuffer[1:n])
        dec := gob.NewDecoder(buffer)
        var recvPath snet.Path
        err = dec.Decode(&recvPath)
        if err != nil{
            fmt.Printf("[Library] Error decoding Path!\n")
            return 0,err
        }

        // newPath := recvPath.Copy()
        // err = newPath.Path().Reverse()
        // if err != nil{
        //     fmt.Printf("[Library] Error reversing path\n")
        //     return 0, err
        // }

        // Set Path to newly received path
        // SetPath(raddr,newPath)
        raddr.Path = recvPath.Path()
        raddr.Path.Reverse()
        inters := recvPath.Metadata().Interfaces
        nextIA := inters[len(inters)-2].IA
        fmt.Printf("[Library] NextIA: %s\n",nextIA)
        pathNextHop,err := QueryPaths(nextIA)
        if err != nil{
            return 0, err
        }

        // raddr.NextHop = recvPath.UnderlayNextHop()
        raddr.NextHop = pathNextHop[0].UnderlayNextHop()
        fmt.Printf("[Library] New Path: %s\n",raddr.Path)

        // create new connection with new path
        fmt.Printf("[Library] Dialing %s\n", raddr)
        err = p.DialAddr(raddr)
        if err != nil {
            return 0,err
        }

        // // send back ok
        // reply := []byte("New Path OK")
        // fmt.Printf("[Library] Sending NewPathOK")
        // n,err = p.Write(reply)
        // if err != nil || n != len(reply){
        //     fmt.Printf("[Library] Error Sending NewPathOK\n")
        //     return 0,err
        // }

        // clear used buffer
        localBuffer = nil

        // switch to new path and read from there
        fmt.Printf("[Library] Recursive Read...\n")
        return p.Read(buf)

    // otherwise check for data
    } else if localBuffer[0] == dataType{
        fmt.Printf("[Library] Got data...")
        // data in buffer
        // copy everything but first byte
        cpN := copy(buf,localBuffer[1:n])
        if cpN != (n-1){
            fmt.Printf("Copied: %d bytes\n",cpN)
            return 0,errFailedCopy
        }
        fmt.Printf("Copied: %d of %d bytes\n",cpN,n)
        fmt.Printf("BufferSize: local = %d\n",len(localBuffer))
        fmt.Printf("BufferSize: remote = %d\n",len(buf))
        return cpN,nil
    }

    // we have received an unknown type
    return 0, errInvalidType

}

// Read from the current connection
func (p *PathNegConn) ReadFrom (buf []byte) (int,net.Addr,error){
    // check if we have a connection to listen from
    if p.listenConn == nil{
        return 0, nil, errNoReadConn
    }
    n, src ,err := p.listenConn.ReadFrom(buf)
    if err != nil{
        return 0, nil, err
    }
    fmt.Printf("[Library] Got Data\n")

    return n, src, nil
}

//write to connection
func (p *PathNegConn) Write(buf []byte) (int,error){
    // check for existance of connection
    if p.currConn == nil {
        return 0,errNoWriteConn
    }

    return p.currConn.Write(buf)
}


//write to destination through listening connection
func (p *PathNegConn) WriteTo(buf []byte, dest net.Addr) (int,error) {
    // check for existance of connection
    if p.listenConn == nil{
        return 0,errNoWriteToConn
    }

    // prepare answer data
    // copy data to new buffer
    l := len(buf)
    localBuffer := make([]byte,l+1)
    cpN := copy(localBuffer[1:],buf)
    if cpN != l {
        return 0,errFailedCopy
    }

    // set type of payload to data
    localBuffer[0] = dataType

    n,err := p.listenConn.WriteTo(localBuffer,dest)
    if err != nil {
        return 0,err
    }
    // return adapted number of written bytes
    return n, nil
}
// func (p *PathNegConn) WriteToFrom(buf []byte, dest *net.Addr) (int,error){
//     // convert address
//     // clientCCAddr := dest.(*snet.UDPAddr)
//     clientCCAddr := dest
//
//     // call WriteTo
//     n,err := p.WriteTo(buf,clientCCAddr)
//     return n,err
// }



func (p *PathNegConn) SendPath(sendPath snet.Path , dest net.Addr) (int,error){
    // check for existance of connection
    if p.listenConn == nil{
        return 0, errNoWriteToConn
    }

    // prepare path data
    var pathBytes bytes.Buffer
    enc := gob.NewEncoder(&pathBytes)
    err := enc.Encode(sendPath)
    if err != nil{
        fmt.Printf("[Library] Error Encoding path\n")
        return 0, err
    }

    // copy data to new buffer
    inbetween := pathBytes.Bytes()
    fmt.Printf("[Library] Sending path, size: %d bytes\n",len(inbetween))
    l := len(inbetween)
    localBuffer := make([]byte,l+1)
    n := copy(localBuffer[1:],inbetween)
    if n != l {
        return 0,errFailedCopy
    }

    // set type of payload to newPath
    localBuffer[0] = newPathType

    n, err = p.listenConn.WriteTo(localBuffer,dest)
    if err != nil {
        return 0, err
    }

    // return adapted number of written bytes
    return (n-1), nil

}


// DialAddr connects to the address (on the SCION/UDP network).
//
// If no path is specified in raddr, DialAddr will choose the first available path.
// This path is never updated during the lifetime of the conn. This does not
// support long lived connections well, as the path *will* expire.
// This is all that snet currently provides, we'll need to add a layer on top
// that updates the paths in case they expire or are revoked.
func dialAddr(raddr *snet.UDPAddr) (*snet.Conn, error) {
	if raddr.Path.IsEmpty() {
		err := SetDefaultPath(raddr)
		if err != nil {
			return nil, err
		}
	}
	localIP, err := resolveLocal(raddr)
	if err != nil {
		return nil, err
	}
	laddr := &net.UDPAddr{IP: localIP}
	return DefNetwork().Dial(context.Background(), "udp", laddr, raddr, addr.SvcNone)
}

func (p *PathNegConn) Listen(addr *net.UDPAddr) error{
    // if we already listening to a connection, 
    // move the current one to the slice
    // use the new one as default
    if p.listenConn != nil{
        p.otherListen = append(p.otherListen,p.listenConn)
    }

    c, err := listen(addr)
    if err != nil {
        return err
    }

    p.listenConn = c

    return nil

}

func (p *PathNegConn) ListenPort(port uint16) error{
    // if we already listening to a connection, 
    // move the current one to the slice
    // use the new one as default
    if p.listenConn != nil{
        p.otherListen = append(p.otherListen,p.listenConn)
    }

    c, err := listenPort(port)
    if err != nil {
        return err
    }

    p.listenConn = c

    return nil

}

// Get all (different) paths to provided addr
func (p *PathNegConn) GetPaths(addr *snet.UDPAddr) ([]snet.Path,error) {
    paths, err := QueryPaths(addr.IA)
    if (err != nil || len(paths) == 0){
        return nil,errPathProb
    }

    return paths,nil
}

// Listen acts like net.ListenUDP in a SCION network.
// The listen address or parts of it may be nil or unspecified, signifying to
// listen on a wildcard address.
//
// See note on wildcard addresses in the package documentation.
func listen(listen *net.UDPAddr) (*snet.Conn, error) {
	if listen == nil {
		listen = &net.UDPAddr{}
	}
	if listen.IP == nil || listen.IP.IsUnspecified() {
		localIP, err := defaultLocalIP()
		if err != nil {
			return nil, err
		}
        fmt.Printf("[Library] DefaultLocalIP to: %s\n",localIP)
		listen = &net.UDPAddr{IP: localIP, Port: listen.Port, Zone: listen.Zone}
	}
    defNetwork := DefNetwork()
    integrationEnv, _ := os.LookupEnv("SCION_GO_INTEGRATION")
    if integrationEnv == "1" || integrationEnv == "true" || integrationEnv == "TRUE" {
        fmt.Printf("Listening ia==:%v\n", defNetwork.IA)
    }
    fmt.Printf("[Library] Listening to: %s\n",listen)
    return defNetwork.Listen(context.Background(), "udp", listen, addr.SvcNone)
}

// ListenPort is a shortcut to Listen on a specific port with a wildcard IP address.
//
// See note on wildcard addresses in the package documentation.
func listenPort(port uint16) (*snet.Conn, error) {
	return listen(&net.UDPAddr{Port: int(port)})
}

// resolveLocal returns the source IP address for traffic to raddr. If
// raddr.NextHop is set, it's used to determine the local IP address.
// Otherwise, the default local IP address is returned.
//
// The purpose of this function is to workaround not being able to bind to
// wildcard addresses in snet.
// See note on wildcard addresses in the package documentation.
func resolveLocal(raddr *snet.UDPAddr) (net.IP, error) {
	if raddr.NextHop != nil {
		nextHop := raddr.NextHop.IP
		return addrutil.ResolveLocal(nextHop)
	}
	return defaultLocalIP()
}

// defaultLocalIP returns _a_ IP of this host in the local AS.
//
// The purpose of this function is to workaround not being able to bind to
// wildcard addresses in snet.
// See note on wildcard addresses in the package documentation.
func defaultLocalIP() (net.IP, error) {
	return addrutil.ResolveLocal(DefNetwork().hostInLocalAS)
}


func initDefNetwork() error {
	ctx, cancel := context.WithTimeout(context.Background(), initTimeout)
	defer cancel()
	dispatcher, err := findDispatcher()
	if err != nil {
		return err
	}
	sciondConn, err := findSciond(ctx)
	if err != nil {
		return err
	}
	localIA, err := sciondConn.LocalIA(ctx)
	if err != nil {
		return err
	}
	hostInLocalAS, err := findAnyHostInLocalAS(ctx, sciondConn)
	if err != nil {
		return err
	}
	pathQuerier := sciond.Querier{Connector: sciondConn, IA: localIA}
	n := snet.NewNetwork(
		localIA,
		dispatcher,
		sciond.RevHandler{Connector: sciondConn},
	)
	dNet = Network{Network: n, IA: localIA, PathQuerier: pathQuerier, hostInLocalAS: hostInLocalAS}
	return nil
}

func findSciond(ctx context.Context) (sciond.Connector, error) {
	address, ok := os.LookupEnv("SCION_DAEMON_ADDRESS")
	if !ok {
		address = sciond.DefaultAPIAddress
	}
	sciondConn, err := sciond.NewService(address).Connect(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to connect to SCIOND at %s (override with SCION_DAEMON_ADDRESS): %w", address, err)
	}
	return sciondConn, nil
}

func findDispatcher() (reliable.Dispatcher, error) {
	path, err := findDispatcherSocket()
	if err != nil {
		return nil, err
	}
	dispatcher := reliable.NewDispatcher(path)
	return dispatcher, nil
}

func findDispatcherSocket() (string, error) {
	path, ok := os.LookupEnv("SCION_DISPATCHER_SOCKET")
	if !ok {
		path = reliable.DefaultDispPath
	}
	err := statSocket(path)
	if err != nil {
		return "", fmt.Errorf("error looking for SCION dispatcher socket at %s (override with SCION_DISPATCHER_SOCKET): %w", path, err)
	}
	return path, nil
}

func statSocket(path string) error {
	fileinfo, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !isSocket(fileinfo.Mode()) {
		return fmt.Errorf("%s is not a socket (mode: %s)", path, fileinfo.Mode())
	}
	return nil
}

func isSocket(mode os.FileMode) bool {
	return mode&os.ModeSocket != 0
}

// findAnyHostInLocalAS returns the IP address of some (infrastructure) host in the local AS.
func findAnyHostInLocalAS(ctx context.Context, sciondConn sciond.Connector) (net.IP, error) {
	addr, err := sciond.TopoQuerier{Connector: sciondConn}.UnderlayAnycast(ctx, addr.SvcCS)
	if err != nil {
		return nil, err
	}
	return addr.IP, nil
}
