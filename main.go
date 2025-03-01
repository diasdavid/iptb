package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	serial "github.com/ipfs/go-ipfs/repo/fsrepo/serialize"

	manet "github.com/jbenet/go-multiaddr-net"
	ma "github.com/jbenet/go-multiaddr-net/Godeps/_workspace/src/github.com/jbenet/go-multiaddr"
)

// GetNumNodes returns the number of testbed nodes configured in the testbed directory
func GetNumNodes() int {
	for i := 0; i < 2000; i++ {
		_, err := os.Stat(IpfsDirN(i))
		if os.IsNotExist(err) {
			return i
		}
	}
	panic("i dont know whats going on")
}

func TestBedDir() string {
	tbd := os.Getenv("IPTB_ROOT")
	if len(tbd) != 0 {
		return tbd
	}

	home := os.Getenv("HOME")
	if len(home) == 0 {
		panic("could not find home")
	}

	return path.Join(home, "testbed")
}

func IpfsDirN(n int) string {
	return path.Join(TestBedDir(), fmt.Sprint(n))
}

func YesNoPrompt(prompt string) bool {
	var s string
	for {
		fmt.Println(prompt)
		fmt.Scanf("%s", &s)
		switch s {
		case "y", "Y":
			return true
		case "n", "N":
			return false
		}
		fmt.Println("Please press either 'y' or 'n'")
	}
}

type initCfg struct {
	Count     int
	Force     bool
	Bootstrap string
	PortStart int
	Mdns      bool
}

func (c *initCfg) swarmAddrForPeer(i int) string {
	return fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", c.PortStart+i)
}

func (c *initCfg) apiAddrForPeer(i int) string {
	return fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", c.PortStart+1000+i)
}

func IpfsInit(cfg *initCfg) error {
	p := IpfsDirN(0)
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		if !cfg.Force && !YesNoPrompt("testbed nodes already exist, overwrite? [y/n]") {
			return nil
		}
		err := os.RemoveAll(TestBedDir())
		if err != nil {
			return err
		}
	}
	wait := sync.WaitGroup{}
	for i := 0; i < cfg.Count; i++ {
		wait.Add(1)
		go func(v int) {
			defer wait.Done()
			dir := IpfsDirN(v)
			err := os.MkdirAll(dir, 0777)
			if err != nil {
				log.Println("ERROR: ", err)
				return
			}

			cmd := exec.Command("ipfs", "init", "-b=1024")
			cmd.Env = append(cmd.Env, "IPFS_PATH="+dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				log.Println("ERROR: ", err)
				log.Println(string(out))
			}
		}(i)
	}
	wait.Wait()

	// Now setup bootstrapping
	switch cfg.Bootstrap {
	case "star":
		err := starBootstrap(cfg)
		if err != nil {
			return err
		}
	case "none":
		err := clearBootstrapping(cfg)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("unrecognized bootstrapping option: %s", cfg.Bootstrap)
	}

	return nil
}

func starBootstrap(icfg *initCfg) error {
	// '0' node is the bootstrap node
	cfgpath := path.Join(IpfsDirN(0), "config")
	bcfg, err := serial.Load(cfgpath)
	if err != nil {
		return err
	}
	bcfg.Bootstrap = nil
	bcfg.Addresses.Swarm = []string{icfg.swarmAddrForPeer(0)}
	bcfg.Addresses.API = icfg.apiAddrForPeer(0)
	bcfg.Addresses.Gateway = ""
	bcfg.Discovery.MDNS.Enabled = icfg.Mdns
	err = serial.WriteConfigFile(cfgpath, bcfg)
	if err != nil {
		return err
	}

	for i := 1; i < icfg.Count; i++ {
		cfgpath := path.Join(IpfsDirN(i), "config")
		cfg, err := serial.Load(cfgpath)
		if err != nil {
			return err
		}

		ba := fmt.Sprintf("%s/ipfs/%s", bcfg.Addresses.Swarm[0], bcfg.Identity.PeerID)
		ba = strings.Replace(ba, "0.0.0.0", "127.0.0.1", -1)
		cfg.Bootstrap = []string{ba}
		cfg.Addresses.Gateway = ""
		cfg.Discovery.MDNS.Enabled = icfg.Mdns
		cfg.Addresses.Swarm = []string{
			icfg.swarmAddrForPeer(i),
		}
		cfg.Addresses.API = icfg.apiAddrForPeer(i)
		err = serial.WriteConfigFile(cfgpath, cfg)
		if err != nil {
			return err
		}
	}
	return nil
}

func clearBootstrapping(icfg *initCfg) error {
	for i := 0; i < icfg.Count; i++ {
		cfgpath := path.Join(IpfsDirN(i), "config")
		cfg, err := serial.Load(cfgpath)
		if err != nil {
			return err
		}

		cfg.Bootstrap = nil
		cfg.Addresses.Gateway = ""
		cfg.Addresses.Swarm = []string{icfg.swarmAddrForPeer(i)}
		cfg.Addresses.API = icfg.apiAddrForPeer(i)
		cfg.Discovery.MDNS.Enabled = icfg.Mdns
		err = serial.WriteConfigFile(cfgpath, cfg)
		if err != nil {
			return err
		}
	}
	return nil
}

func IpfsPidOf(n int) (int, error) {
	dir := IpfsDirN(n)
	b, err := ioutil.ReadFile(path.Join(dir, "daemon.pid"))
	if err != nil {
		return -1, err
	}

	return strconv.Atoi(string(b))
}

func IpfsKill() error {
	n := GetNumNodes()
	for i := 0; i < n; i++ {
		pid, err := IpfsPidOf(i)
		if err != nil {
			fmt.Printf("error killing daemon %d: %s\n", i, err)
			continue
		}

		p, err := os.FindProcess(pid)
		if err != nil {
			fmt.Printf("error killing daemon %d: %s\n", i, err)
			continue
		}
		err = p.Kill()
		if err != nil {
			fmt.Printf("error killing daemon %d: %s\n", i, err)
			continue
		}

		p.Wait()

		err = os.Remove(path.Join(IpfsDirN(i), "daemon.pid"))
		if err != nil {
			fmt.Printf("error removing pid file for daemon %d: %s\n", i, err)
			continue
		}
	}
	return nil
}

func IpfsStart(waitall bool) error {
	var addrs []string
	n := GetNumNodes()
	for i := 0; i < n; i++ {
		dir := IpfsDirN(i)
		cmd := exec.Command("ipfs", "daemon")
		cmd.Dir = dir
		cmd.Env = []string{"IPFS_PATH=" + dir}

		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

		stdout, err := os.Create(path.Join(dir, "daemon.stdout"))
		if err != nil {
			return err
		}

		stderr, err := os.Create(path.Join(dir, "daemon.stderr"))
		if err != nil {
			return err
		}

		cmd.Stdout = stdout
		cmd.Stderr = stderr

		err = cmd.Start()
		if err != nil {
			return err
		}
		pid := cmd.Process.Pid

		fmt.Printf("Started daemon %d, pid = %d\n", i, pid)
		err = ioutil.WriteFile(path.Join(dir, "daemon.pid"), []byte(fmt.Sprint(pid)), 0666)
		if err != nil {
			return err
		}

		// Make sure node 0 is up before starting the rest so
		// bootstrapping works properly
		if i == 0 || waitall {
			cfg, err := serial.Load(path.Join(IpfsDirN(i), "config"))
			if err != nil {
				return err
			}

			maddr := ma.StringCast(cfg.Addresses.API)
			_, addr, err := manet.DialArgs(maddr)
			if err != nil {
				return err
			}

			addrs = append(addrs, addr)

			err = waitOnAPI(cfg.Identity.PeerID, addr)
			if err != nil {
				return err
			}
		}
	}
	if waitall {
		for i := 0; i < n; i++ {
			err := waitOnSwarmPeers(addrs[i])
			if err != nil {
				return err
			}
		}

	}
	return nil
}

func waitOnAPI(peerid, addr string) error {
	for i := 0; i < 50; i++ {
		resp, err := http.Get("http://" + addr + "/api/v0/id")
		if err == nil {
			out := make(map[string]interface{})
			err := json.NewDecoder(resp.Body).Decode(&out)
			if err != nil {
				return fmt.Errorf("liveness check failed: %s", err)
			}
			id, ok := out["ID"]
			if !ok {
				return fmt.Errorf("liveness check failed: ID field not present in output")
			}
			idstr := id.(string)
			if idstr != peerid {
				return fmt.Errorf("liveness check failed: unexpected peer at endpoint")
			}

			return nil
		}
		time.Sleep(time.Millisecond * 200)
	}
	return fmt.Errorf("node at %s failed to come online in given time period", addr)
}

func waitOnSwarmPeers(addr string) error {
	for i := 0; i < 50; i++ {
		resp, err := http.Get("http://" + addr + "/api/v0/swarm/peers")
		if err == nil {
			out := make(map[string]interface{})
			err := json.NewDecoder(resp.Body).Decode(&out)
			if err != nil {
				return fmt.Errorf("liveness check failed: %s", err)
			}

			peers := out["Strings"].([]interface{})
			if len(peers) == 0 {
				time.Sleep(time.Millisecond * 200)
				continue
			}

			return nil
		}
		time.Sleep(time.Millisecond * 200)
	}
	return fmt.Errorf("node at %s failed to bootstrap in given time period", addr)
}

// GetPeerID reads the config of node 'n' and returns its peer ID
func GetPeerID(n int) (string, error) {
	cfg, err := serial.Load(path.Join(IpfsDirN(n), "config"))
	if err != nil {
		return "", err
	}
	return cfg.Identity.PeerID, nil
}

// IpfsShell sets up environment variables for a new shell to more easily
// control the given daemon
func IpfsShell(n int) error {
	shell := os.Getenv("SHELL")
	if shell == "" {
		return fmt.Errorf("couldnt find shell!")
	}

	dir := IpfsDirN(n)
	nenvs := []string{"IPFS_PATH=" + dir}

	nnodes := GetNumNodes()
	for i := 0; i < nnodes; i++ {
		peerid, err := GetPeerID(i)
		if err != nil {
			return err
		}
		nenvs = append(nenvs, fmt.Sprintf("NODE%d=%s", i, peerid))
	}
	nenvs = append(os.Environ(), nenvs...)

	return syscall.Exec(shell, []string{shell}, nenvs)
}

func GetAttr(attr string, node int) (string, error) {
	switch attr {
	case "id":
		return GetPeerID(node)
	default:
		return "", errors.New("unrecognized attribute")
	}
}

var helptext = `Ipfs Testbed

Commands:
	init 
	    creates and initializes 'n' repos

		Options:
			-n=[number of nodes]
			-f - force overwriting of existing nodes
			-bootstrap - select bootstrapping style for cluster
				choices: star, none

	start 
	    starts up all testbed nodes

		Options:
			-wait - wait until daemons are fully initialized
	stop 
	    kills all testbed nodes
	restart
	    kills, then restarts all testbed nodes

	shell [n]
	    execs your shell with environment variables set as follows:
	        IPFS_PATH - set to testbed node n's IPFS_PATH
	        NODE[x] - set to the peer ID of node x

	get [attribute] [node]
		get an attribute of the given node
		currently supports: "id"

Env Vars:

IPTB_ROOT:
	Used to specify the directory that nodes will be created in.
`

func handleErr(s string, err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, s, err)
		os.Exit(1)
	}
}

func main() {
	cfg := new(initCfg)
	flag.IntVar(&cfg.Count, "n", 0, "number of ipfs nodes to initialize")
	flag.IntVar(&cfg.PortStart, "p", 4002, "port to start allocations from")
	flag.BoolVar(&cfg.Force, "f", false, "force initialization (overwrite existing configs)")
	flag.BoolVar(&cfg.Mdns, "mdns", false, "turn on mdns for nodes")
	flag.StringVar(&cfg.Bootstrap, "bootstrap", "star", "select bootstrapping style for cluster")

	wait := flag.Bool("wait", false, "wait for nodes to come fully online before exiting")
	flag.Usage = func() {
		fmt.Println(helptext)
	}

	flag.Parse()

	switch flag.Arg(0) {
	case "init":
		if cfg.Count == 0 {
			fmt.Printf("please specify number of nodes: '%s -n=10 init'\n", os.Args[0])
			os.Exit(1)
		}
		err := IpfsInit(cfg)
		handleErr("ipfs init err: ", err)
	case "start":
		err := IpfsStart(*wait)
		handleErr("ipfs start err: ", err)
	case "stop", "kill":
		err := IpfsKill()
		handleErr("ipfs kill err: ", err)
	case "restart":
		err := IpfsKill()
		handleErr("ipfs kill err: ", err)

		err = IpfsStart(*wait)
		handleErr("ipfs start err: ", err)
	case "shell":
		if len(flag.Args()) < 2 {
			fmt.Println("please specify which node you want a shell for")
			os.Exit(1)
		}
		n, err := strconv.Atoi(flag.Arg(1))
		handleErr("parse err: ", err)

		err = IpfsShell(n)
		handleErr("ipfs shell err: ", err)
	case "get":
		if len(flag.Args()) < 3 {
			fmt.Println("iptb get [attr] [node]")
			os.Exit(1)
		}
		attr := flag.Arg(1)
		num, err := strconv.Atoi(flag.Arg(2))
		handleErr("error parsing node number: ", err)

		val, err := GetAttr(attr, num)
		handleErr("error getting attribute: ", err)
		fmt.Println(val)
	default:
		flag.Usage()
		os.Exit(1)
	}
}
