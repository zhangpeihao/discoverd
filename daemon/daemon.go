// Package daemon implements server config discover.
package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/coreos/go-etcd/etcd"
	"log"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// Server etcd keys definition
// <perfix> / servers / <server ID> / config / lock ------ server configuration lock (with TTL)
//                                           / details --- server configuration details (JSON format)
//                                                         {service_instances:[name:string, id:string]}
//                                  / state -------------- server running state (with TTL)
//
// Service keys definition
// <prefix> / services / <service name> / instances / <service ID> / config / lock ----- service instance configuration lock (with TTL)
//                                                                          / details -- running parameters
//                                                                 / state ------------- service instance running state (with TTL)
//                                      / versions / <version> / config / lock --------- service config lock (with TTL)
//                                                                      / details ------ download and install parameters
//                                                                                       running common parameters

const (
	DAEMON_STATE_INIT = iota
	DAEMON_STATE_CONNECTING_ETCD
)

const (
	STATE_LOADING_CONFIGURATION = "loading_configuration"
	STATE_DOWNLOADING           = "downloading"
	STATE_INSTALLING            = "installing"
	STATE_RUNNING               = "running"
	STATE_STOPPING              = "STOPPING"
	STATE_EXIT                  = "exit"
)

var (
	ErrKillProcessTimeout = errors.New("Kill process timeout")
	ErrKillProcessFailure = errors.New("Kill process failure")
	ErrProcessExited      = errors.New("Process exited")
	ErrProcessTimeout     = errors.New("Process timeout")
)

type DaemonParameters struct {
	ServerId            string
	EtcdAddresses       []string
	EtcdKeyPrefix       string
	UpdateStateInterval uint64
}

type Daemon struct {
	params          DaemonParameters
	updateStateTtl  uint64
	exit_chan       chan bool
	exited_chan     chan bool
	config_chan     chan *etcd.Response
	service_chan    chan *ServiceCommand
	goRoutineNumber int32
	state           ServerState
}

type ServerState struct {
	UpdateTime int64   `json:"update_time"`
	State      string  `json:"state"`
	Failure    int     `json:"failure"`
	Load       float32 `json:"load"`
}

type ServerConfig struct {
	ServiceInstances []ServiceInstanceBriefConfig `json:"service_instances"`
}

type ServiceInstanceBriefConfig struct {
	Name string `json:"name"`
	Id   string `json:"id"`
}

type ServiceScript struct {
	Command string   `json:"command"`
	Argv    []string `json:"argv"`
	Timeout int      `json:"timeout"`
}

type ServiceConfig struct {
	Name           string        `json:"name"`
	ServiceVersion string        `json:-`
	DownloadScript ServiceScript `json:"download",omitempty`
	InstallScript  ServiceScript `json:"install",omitempty`
	RunScript      ServiceScript `json:"run",omitempty`
}

type ServiceInstanceConfig struct {
	Name           string        `json:"name"`
	ServiceVersion string        `json:"service_version"`
	Id             string        `json:-`
	RunScript      ServiceScript `json:"run",omitempty`
}

type ServiceInstanceState struct {
	ServiceVersion string      `json:"service_version"`
	UpdateTime     int64       `json:"update_time"`
	State          string      `json:"state"`
	Load           float32     `json:"load"`
	Failure        int         `json:"failure"`
	Process        *os.Process `json:-`
}

type ServiceInstanceData struct {
	ServiceConfig  ServiceConfig
	InstanceConfig ServiceInstanceConfig
	State          ServiceInstanceState
	exit_chan      chan bool
	prevIndex      uint64
}

type ServiceCommand struct {
	Command string
	Message string
}

func NewDaemon(params *DaemonParameters) *Daemon {
	daemon := &Daemon{
		params:         *params,
		updateStateTtl: params.UpdateStateInterval * 3,
		exit_chan:      make(chan bool, 10),
		exited_chan:    make(chan bool, 10),
		config_chan:    make(chan *etcd.Response, 10),
		service_chan:   make(chan *ServiceCommand, 100),
		state: ServerState{
			State:      STATE_LOADING_CONFIGURATION,
			UpdateTime: time.Now().Unix(),
		},
	}

	if len(daemon.params.EtcdKeyPrefix) > 0 && !strings.HasPrefix(daemon.params.EtcdKeyPrefix, `/`) {
		daemon.params.EtcdKeyPrefix = `/` + daemon.params.EtcdKeyPrefix
	}
	return daemon
}

func (daemon *Daemon) SafeExit(timeout int) {
	log.Printf("SafeExit(%d)\ndaemon.goRoutineNumber: %d\n", timeout, daemon.goRoutineNumber)
	for i := int32(0); i < daemon.goRoutineNumber; i++ {
		daemon.exit_chan <- true
	}
	tick := time.NewTicker(time.Duration(timeout) * time.Second)
TOP_LOOP:
	for {
		select {
		case <-daemon.exited_chan:
			goRoutineNumber := atomic.LoadInt32(&daemon.goRoutineNumber)
			if goRoutineNumber == 0 {
				break TOP_LOOP
			}
		case <-tick.C:
			log.Println("SafeExit() timeout")
			break TOP_LOOP
		}
	}
}

func (daemon *Daemon) Run() {
	if daemon.goRoutineNumber == 0 {
		go daemon.updateServerState()
		go daemon.watchServerConfig()
		go daemon.mainLoop()
	}
}

func (daemon *Daemon) IncreaseRoutineNumber() {
	atomic.AddInt32(&daemon.goRoutineNumber, 1)
}

func (daemon *Daemon) DecreaseRoutineNumber() {
	atomic.AddInt32(&daemon.goRoutineNumber, -1)
	daemon.exited_chan <- true
}

func (daemon *Daemon) updateServerState() {
	etcdClient := etcd.NewClient(daemon.params.EtcdAddresses)
	key := daemon.serverKey("state")
	sleepDuration := time.Millisecond * 100
	nodeCreated := false
	var state string
	var lastSetTime int64
	var prevIndex uint64
	errorCounter := 0
	var err error
	var etcdResponse *etcd.Response

	log.Println("updateServerState() begin")
	daemon.IncreaseRoutineNumber()
	defer func() {
		daemon.DecreaseRoutineNumber()
		log.Println("updateServerState() routine exit")
	}()

TOP_LOOP:
	for {
		select {
		case <-daemon.exit_chan:
			// Remove state
			log.Println("updateServerState() got exit_chan, remove state from etcd")
			etcdClient.CompareAndDelete(key, "", prevIndex)
			break TOP_LOOP
		case <-time.After(sleepDuration):
			state = daemon.getState()
			if nodeCreated {
				log.Println("updateServerState() update state")
				// Update state node
				state = daemon.getState()
				etcdResponse, err = etcdClient.CompareAndSwap(key, state, daemon.updateStateTtl,
					"", prevIndex)
				if err != nil {
					log.Printf("etcd - CompareAndSwap %s err: %s\n", key, err.Error())
					switch getEtcdErrorCode(err) {
					case ErrCodeEtcdNotReachable:
						time.Sleep(2 * time.Second)
						if lastSetTime+int64(daemon.updateStateTtl) <= time.Now().Unix() {
							daemon.service_chan <- &ServiceCommand{
								Command: "alert",
								Message: fmt.Sprintf("Server %s con not connect etcd server", daemon.params.ServerId),
							}
							nodeCreated = false
							sleepDuration = time.Second
							continue TOP_LOOP
						}
						continue
					case EcodeKeyNotFound:
						// Serve conflicted
						daemon.service_chan <- &ServiceCommand{
							Command: "alert",
							Message: fmt.Sprintf("Server %s conflicted on etcd", daemon.params.ServerId),
						}
						nodeCreated = false
						sleepDuration = time.Second
						continue TOP_LOOP
					default:
						errorCounter++
						if errorCounter > 3 {
							daemon.service_chan <- &ServiceCommand{
								Command: "alert",
								Message: fmt.Sprintf("Server %s con not update state on etcd", daemon.params.ServerId),
							}
							nodeCreated = false
							sleepDuration = time.Second
							continue TOP_LOOP
						}
						sleepDuration = 2 * time.Second
						continue TOP_LOOP
					}
				}
				sleepDuration = time.Duration(daemon.params.UpdateStateInterval) * time.Second
			} else {
				log.Println("updateServerState() create state node")
				// Create state node
				etcdResponse, err = etcdClient.Create(key, state, daemon.updateStateTtl)
				if err != nil {
					log.Printf("etcd - SET %s err: %s\n", key, err.Error())
					daemon.service_chan <- &ServiceCommand{
						Command: "alert",
						Message: fmt.Sprintf("Server %s con not create state on etcd", daemon.params.ServerId),
					}
					sleepDuration = time.Minute
					continue TOP_LOOP
				}
				nodeCreated = true
				sleepDuration = time.Millisecond * 100
			}
			if etcdResponse == nil {
				log.Printf("etcd - SET %s response is nil\n", key)
				sleepDuration = time.Minute
				continue TOP_LOOP
			}
			prevIndex = etcdResponse.Node.ModifiedIndex
			errorCounter = 0
			lastSetTime = time.Now().Unix()
		}
	}
}

func (daemon *Daemon) watchServerConfig() {
	var err error
	var etcdResponse *etcd.Response
	var lastModifiedIndex uint64

	log.Println("watchServerConfig() begin")
	daemon.IncreaseRoutineNumber()
	defer func() {
		daemon.DecreaseRoutineNumber()
		log.Println("watchServerConfig() routine exit")
	}()

	etcdClient := etcd.NewClient(daemon.params.EtcdAddresses)
	key := daemon.serverKey("config/details")
	sleepDuration := time.Millisecond * 100
	watching := false
	watchExitChan := make(chan bool, 10)

TOP_LOOP:
	for {
		select {
		case <-daemon.exit_chan:
			// Remove state
			break TOP_LOOP
		case <-time.After(sleepDuration):
			if !watching {
				etcdResponse, err = etcdClient.Get(key, false, true)
				if err != nil {
					switch getEtcdErrorCode(err) {
					case EcodeKeyNotFound:
						fallthrough
					case EcodeNodeExist:
						// Server configuration not existed.
						log.Printf("Server(%s) configuration not existed. err: %s\n", daemon.params.ServerId, err.Error())
						sleepDuration = time.Second * 30

					default:
						log.Printf("etcd.Get(%s) err: %s\n", key, err.Error())
						sleepDuration = time.Minute
					}
					daemon.state.Failure++
					daemon.state.UpdateTime = time.Now().Unix()
					continue TOP_LOOP
				}
				lastModifiedIndex = etcdResponse.Node.ModifiedIndex
				daemon.state.State = STATE_RUNNING
				daemon.state.Failure = 0
				daemon.state.UpdateTime = time.Now().Unix()
				daemon.config_chan <- etcdResponse
				watching = true
				go func() {
					log.Println("etcd watch routine() begin")
					daemon.IncreaseRoutineNumber()
					defer func() {
						daemon.DecreaseRoutineNumber()
						watching = false
						watchExitChan <- true
						log.Println("etcd watch routine exit")
					}()
					etcdClient.Watch(key, lastModifiedIndex+1, true, daemon.config_chan, daemon.exit_chan)
				}()
				sleepDuration = time.Minute * 30
			} else {
				sleepDuration = time.Minute * 30
			}
		case <-watchExitChan:
			sleepDuration = time.Second
		}
	}
}

func (daemon *Daemon) mainLoop() {
	var err error
	var etcdResponse *etcd.Response
	var serviceInstanceKey string
	var serviceInstanceConfig *ServiceInstanceConfig
	var serviceInstanceData *ServiceInstanceData
	var found, success bool
	var key string

	etcdClient := etcd.NewClient(daemon.params.EtcdAddresses)
	serviceInstances := make(map[string]*ServiceInstanceData)

	log.Println("mainLoop() begin")
	daemon.IncreaseRoutineNumber()
	defer func() {
		daemon.DecreaseRoutineNumber()
		log.Println("mainLoop() routine exit")
	}()

TOP_LOOP:
	for {
		select {
		case <-daemon.exit_chan:
			// Stop all service instances
			for _, serviceInstanceData := range serviceInstances {
				serviceInstanceData.exit_chan <- true
			}
		EXIT_CHECK_LOOP:
			for i := 0; i < 10; i++ {
				time.Sleep(100 * time.Millisecond)
				for _, serviceInstanceData := range serviceInstances {
					if serviceInstanceData.State.State != STATE_EXIT {
						continue EXIT_CHECK_LOOP
					}
				}
				log.Println("All service instances exit normally")
				break EXIT_CHECK_LOOP
			}
			break TOP_LOOP
		case etcdResponse = <-daemon.config_chan:
			log.Printf("got config: %s\n", etcdResponse.Node.Value)
			var newServerConfig ServerConfig
			err = json.Unmarshal([]byte(etcdResponse.Node.Value), &newServerConfig)
			if err != nil {
				log.Printf("Unmarshal server config err: %s\nconfig string: %s\n", err.Error(), etcdResponse.Node.Value)
				continue TOP_LOOP
			}

			newServiceInstances := make(map[string]*ServiceInstanceConfig)
			for _, sbi := range newServerConfig.ServiceInstances {
				// Get service instance config
				key = daemon.serviceInstanceKey(sbi.Name, sbi.Id,
					`config/details`)
				etcdResponse, err = etcdClient.Get(key, false, false)
				if err != nil {
					log.Printf("Get service %s instance(ID %s) config from etcd error: %s\n",
						sbi.Name, sbi.Id,
						err.Error())
					daemon.state.Failure++
					daemon.state.UpdateTime = time.Now().Unix()
					continue TOP_LOOP
				}
				var si ServiceInstanceConfig
				serviceInstanceConfig = &si
				err = json.Unmarshal([]byte(etcdResponse.Node.Value), serviceInstanceConfig)
				if err != nil {
					log.Printf("Service %s instance(ID %s) config unmarshal error: %s\n",
						sbi.Name, sbi.Id,
						err.Error())
					daemon.state.Failure++
					daemon.state.UpdateTime = time.Now().Unix()
					continue TOP_LOOP
				}
				serviceInstanceConfig.Id = sbi.Id

				serviceInstanceKey = ServiceInstanceKey(serviceInstanceConfig)
				newServiceInstances[serviceInstanceKey] = serviceInstanceConfig
			}

			for serviceInstanceKey, serviceInstanceData = range serviceInstances {
				if _, found = newServiceInstances[serviceInstanceKey]; !found {
					log.Printf("Remove service instance: %s\n", serviceInstanceKey)
					delete(serviceInstances, serviceInstanceKey)
					serviceInstanceData.exit_chan <- true
				}
			}

			success = true
			for serviceInstanceKey, serviceInstanceConfig = range newServiceInstances {
				if _, found = serviceInstances[serviceInstanceKey]; !found {
					log.Printf("New service instance: %s\n", serviceInstanceKey)
					serviceInstanceData := &ServiceInstanceData{
						exit_chan:      make(chan bool),
						InstanceConfig: *serviceInstanceConfig,
					}
					// Get service config
					key = daemon.serviceVersionKey(serviceInstanceConfig.Name, serviceInstanceConfig.ServiceVersion,
						`config/details`)
					etcdResponse, err = etcdClient.Get(key, false, false)
					if err != nil {
						log.Printf("Get service %s(version %s) config from etcd error: %s\n",
							serviceInstanceConfig.Name, serviceInstanceConfig.ServiceVersion,
							err.Error())
						success = false
						continue
					}
					err = json.Unmarshal([]byte(etcdResponse.Node.Value), &(serviceInstanceData.ServiceConfig))
					if err != nil {
						log.Printf("Service %s(version %s) config unmarshal error: %s\n",
							serviceInstanceConfig.Name, serviceInstanceConfig.ServiceVersion,
							err.Error())
						success = false
						continue
					}
					serviceInstanceData.ServiceConfig.ServiceVersion = serviceInstanceConfig.ServiceVersion

					serviceInstanceData.State.ServiceVersion = serviceInstanceConfig.ServiceVersion
					serviceInstanceData.State.State = STATE_LOADING_CONFIGURATION
					serviceInstances[serviceInstanceKey] = serviceInstanceData
					go daemon.serviceMaintainLoop(serviceInstanceData)
				}
			}
			if success {
				if daemon.state.Failure != 0 {
					daemon.state.Failure = 0
					daemon.state.UpdateTime = time.Now().Unix()
				}
			} else {
				daemon.state.Failure++
				daemon.state.UpdateTime = time.Now().Unix()
			}
		case serviceCommand := <-daemon.service_chan:
			switch serviceCommand.Command {
			case "alert":
				// Todo: Alert
			}
		}
	}
}

func (daemon *Daemon) deleteEtcdServiceInstanceState(data *ServiceInstanceData) {
	if data.prevIndex != 0 {
		etcdClient := etcd.NewClient(daemon.params.EtcdAddresses)
		key := daemon.serviceInstanceKey(data.InstanceConfig.Name, data.InstanceConfig.Id,
			"state")
		etcdClient.CompareAndDelete(key, "", data.prevIndex)
	}
}

func (daemon *Daemon) updateEtcdServiceInstanceState(data *ServiceInstanceData) {
	var etcdResponse *etcd.Response
	var err error
	var stateBytes []byte
	var state string
	var alertMessage string
	etcdClient := etcd.NewClient(daemon.params.EtcdAddresses)
	key := daemon.serviceInstanceKey(data.InstanceConfig.Name, data.InstanceConfig.Id,
		"state")
	stateBytes, err = json.Marshal(data.State)
	if err != nil {
		alertMessage = fmt.Sprintf("service %s(ID: %s, Version: %s) json.Marshal err: %s\n",
			data.InstanceConfig.Name, data.InstanceConfig.Id,
			data.InstanceConfig.ServiceVersion, err.Error())
		log.Println(alertMessage)
		daemon.service_chan <- &ServiceCommand{
			Command: "alert",
			Message: alertMessage,
		}
		return
	}
	state = string(stateBytes)
	if data.prevIndex == 0 {
		// Create
		etcdResponse, err = etcdClient.Create(key, state, daemon.updateStateTtl)
		if err != nil {
			alertMessage = fmt.Sprintf("service %s(ID: %s, Version: %s) create etcd state node %s err: %s\n",
				data.InstanceConfig.Name, data.InstanceConfig.Id,
				data.InstanceConfig.ServiceVersion, key, err.Error())
			log.Println(alertMessage)
			daemon.service_chan <- &ServiceCommand{
				Command: "alert",
				Message: alertMessage,
			}
			return
		}
	} else {
		// Update
		etcdResponse, err = etcdClient.CompareAndSwap(key, state, daemon.updateStateTtl,
			"", data.prevIndex)
		if err != nil {
			alertMessage = fmt.Sprintf("service %s(ID: %s, Version: %s) update etcd state node %s err: %s\n",
				data.InstanceConfig.Name, data.InstanceConfig.Id,
				data.InstanceConfig.ServiceVersion, key, err.Error())
			log.Println(alertMessage)
			daemon.service_chan <- &ServiceCommand{
				Command: "alert",
				Message: alertMessage,
			}
			data.prevIndex = 0
			return
		}
	}
	data.prevIndex = etcdResponse.Node.ModifiedIndex
}

func (daemon *Daemon) serviceFailure(data *ServiceInstanceData, err error) {
	msg := fmt.Sprintf("service %s(ID: %s, Version: %s) %s err: %s",
		data.InstanceConfig.Name, data.InstanceConfig.Id,
		data.InstanceConfig.ServiceVersion, data.State.State, err.Error())
	log.Println(msg)
	data.State.Failure++
	daemon.updateEtcdServiceInstanceState(data)
	daemon.service_chan <- &ServiceCommand{
		Command: "alert",
		Message: msg,
	}
}

func (daemon *Daemon) serviceMaintainLoop(data *ServiceInstanceData) {
	var err error
	var command string
	var argv []string
	var process *os.Process

	tickTime := time.Duration(daemon.params.UpdateStateInterval) * time.Second
	exited_chan := make(chan error, 1)
	sleepTime := time.Millisecond * 10
	data.State.State = STATE_DOWNLOADING

	daemon.updateEtcdServiceInstanceState(data)
	defer daemon.deleteEtcdServiceInstanceState(data)

TOP_LOOP:
	for {
		select {
		case <-data.exit_chan:
			log.Printf("Service %s instance(ID: %s) exit\n", data.InstanceConfig.Name,
				data.InstanceConfig.Id)
			// Stop this service instance
			switch data.State.State {
			case STATE_RUNNING:
				if data.State.Process != nil {
					data.State.State = STATE_STOPPING
					data.State.Process.Signal(syscall.SIGINT)
					sleepTime = time.Second * 5
				} else {
					break TOP_LOOP
				}
			case STATE_DOWNLOADING:
				fallthrough
			case STATE_INSTALLING:
				if process != nil {
					data.State.State = STATE_STOPPING
					process.Signal(syscall.SIGINT)
					sleepTime = time.Second * 5
				} else {
					break TOP_LOOP
				}
			default:
				break TOP_LOOP
			}
		case err = <-exited_chan:
			// Process finished
			if err != nil {
				switch data.State.State {
				case STATE_DOWNLOADING:
					fallthrough
				case STATE_INSTALLING:
					process = nil
					daemon.serviceFailure(data, err)
					sleepTime = time.Second * 5
				case STATE_RUNNING:
					data.State.Process = nil
					daemon.serviceFailure(data, err)
					sleepTime = time.Second * 5
				case STATE_STOPPING:
					log.Printf("Service %s instance(ID: %s) Process safe exited with err: %s\n",
						data.InstanceConfig.Name, data.InstanceConfig.Id, err.Error())
					break TOP_LOOP
				default:
					log.Printf("Service %s instance(ID: %s) ERROR STATE MACHINE: event: process_finished_failure, state: %s\n",
						data.InstanceConfig.Name, data.InstanceConfig.Id,
						data.State.State)
				}
			} else {
				switch data.State.State {
				case STATE_DOWNLOADING:
					log.Printf("Service %s instance(ID: %s) Download Process finished, State to INSTALLING\n",
						data.InstanceConfig.Name, data.InstanceConfig.Id)
					data.State.State = STATE_INSTALLING
					sleepTime = time.Millisecond * 10
					process = nil
				case STATE_INSTALLING:
					log.Printf("Service %s instance(ID: %s) Install Process finished, State to RUNNING\n",
						data.InstanceConfig.Name, data.InstanceConfig.Id)
					data.State.State = STATE_RUNNING
					sleepTime = time.Millisecond * 10
					process = nil
				case STATE_RUNNING:
					log.Printf("Service %s instance(ID: %s) Main Process exited try to restart\n",
						data.InstanceConfig.Name, data.InstanceConfig.Id)
					daemon.serviceFailure(data, ErrProcessExited)
					sleepTime = time.Second * 5
					data.State.Process = nil
				case STATE_STOPPING:
					log.Printf("Service %s instance(ID: %s) Process safe exited\n",
						data.InstanceConfig.Name, data.InstanceConfig.Id)
					break TOP_LOOP
				default:
					log.Printf("Service %s instance(ID: %s) ERROR STATE MACHINE: event: process_finished_success, state: %s\n",
						data.InstanceConfig.Name, data.InstanceConfig.Id,
						data.State.State)
				}
			}
		case <-time.After(sleepTime):
			// Main state machine
			switch data.State.State {
			case STATE_DOWNLOADING:
				if len(data.ServiceConfig.DownloadScript.Command) > 0 {
					log.Printf("Service %s instance(ID: %s) download script: %s %+v\n",
						data.InstanceConfig.Name, data.InstanceConfig.Id,
						data.ServiceConfig.DownloadScript.Command,
						data.ServiceConfig.DownloadScript.Argv)
					if data.ServiceConfig.DownloadScript.Timeout > 0 {
						process, err = RunProcessWithTimeout(data.ServiceConfig.DownloadScript.Command,
							data.ServiceConfig.DownloadScript.Argv, exited_chan,
							time.Duration(data.ServiceConfig.DownloadScript.Timeout)*time.Second)

					} else {
						process, err = RunProcess(data.ServiceConfig.DownloadScript.Command,
							data.ServiceConfig.DownloadScript.Argv, exited_chan)
					}
					if err != nil {
						daemon.serviceFailure(data, err)
						sleepTime = time.Second * 5
					} else {
						// Wait process finish
						sleepTime = time.Duration(math.MaxInt64)
					}
				} else {
					data.State.State = STATE_INSTALLING
					sleepTime = time.Millisecond * 10
				}
			case STATE_INSTALLING:
				if len(data.ServiceConfig.InstallScript.Command) > 0 {
					log.Printf("Service %s instance(ID: %s) install script: %s %+v\n",
						data.InstanceConfig.Name, data.InstanceConfig.Id,
						data.ServiceConfig.InstallScript.Command,
						data.ServiceConfig.InstallScript.Argv)
					if data.ServiceConfig.InstallScript.Timeout > 0 {
						process, err = RunProcessWithTimeout(data.ServiceConfig.InstallScript.Command,
							data.ServiceConfig.InstallScript.Argv, exited_chan,
							time.Duration(data.ServiceConfig.InstallScript.Timeout)*time.Second)
					} else {
						process, err = RunProcess(data.ServiceConfig.InstallScript.Command,
							data.ServiceConfig.InstallScript.Argv, exited_chan)
					}
					if err != nil {
						daemon.serviceFailure(data, err)
						sleepTime = time.Second * 5
					} else {
						// Wait process finish
						sleepTime = time.Duration(math.MaxInt64)
					}
				} else {
					data.State.State = STATE_RUNNING
					sleepTime = time.Millisecond * 10
				}
			case STATE_RUNNING:
				if len(data.InstanceConfig.RunScript.Command) > 0 {
					command = data.InstanceConfig.RunScript.Command
				} else {
					command = data.ServiceConfig.RunScript.Command
				}
				argv = append(data.ServiceConfig.RunScript.Argv, data.InstanceConfig.RunScript.Argv...)
				log.Printf("Service %s instance(ID: %s) run script: %s %+v\n",
					data.InstanceConfig.Name, data.InstanceConfig.Id,
					command, argv)
				data.State.Process, err = RunProcess(command, argv, exited_chan)
				if err != nil {
					daemon.serviceFailure(data, err)
					sleepTime = time.Second * 5
				} else {
					// Wait process finish
					sleepTime = time.Duration(math.MaxInt64)
				}
			case STATE_STOPPING:
				log.Printf("Service %s instance(ID: %s) Safe stop process timeout\n",
					data.InstanceConfig.Name, data.InstanceConfig.Id)
				if data.State.Process != nil {
					data.State.Process.Kill()
				}
				if process != nil {
					process.Kill()
				}
				break TOP_LOOP
			default:
				log.Printf("Service %s instance(ID: %s) ERROR STATE MACHINE: event: timer, state: %s\n",
					data.InstanceConfig.Name, data.InstanceConfig.Id, data.State.State)
			}
		case <-time.Tick(tickTime):
			daemon.updateEtcdServiceInstanceState(data)
		}
	}
	data.State.State = STATE_EXIT
}

func (daemon *Daemon) getState() string {
	stateBytes, err := json.Marshal(&daemon.state)
	if err != nil {
		return ""
	}
	return string(stateBytes)
}

func (daemon *Daemon) serverKey(path string) string {
	return daemon.params.EtcdKeyPrefix + `/servers/` + daemon.params.ServerId + `/` + path
}

func (daemon *Daemon) serviceKey(service_name, path string) string {
	return daemon.params.EtcdKeyPrefix + `/services/` + service_name + `/` + path
}

func (daemon *Daemon) serviceInstanceKey(service_name, serviceId, path string) string {
	return daemon.serviceKey(service_name, `instances/`+serviceId+`/`+path)
}

func (daemon *Daemon) serviceVersionKey(service_name, version, path string) string {
	return daemon.serviceKey(service_name, `versions/`+version+`/`+path)
}

func RunProcess(command string, argv []string, exited_chan chan<- error) (process *os.Process, err error) {
	cmd := exec.Command(command, argv...)
	err = cmd.Start()
	if err != nil {
		return
	}
	process = cmd.Process
	go func() {
		processState, err := process.Wait()
		if err == nil && !processState.Success() {
			log.Printf("Process exit with state: %s\n", processState.String())
			err = ErrKillProcessFailure
		}
		exited_chan <- err
	}()

	return
}

func RunProcessWithTimeout(command string, argv []string, exited_chan chan<- error,
	timeout time.Duration) (process *os.Process, err error) {
	cmd := exec.Command(command, argv...)
	err = cmd.Start()
	if err != nil {
		return
	}
	process = cmd.Process
	local_chan := make(chan error)
	go func() {
		processState, err := process.Wait()
		if err == nil && !processState.Success() {
			log.Printf("SafeKillProcess() result: %+v\n", processState)
			err = ErrKillProcessFailure
		}
		local_chan <- err
	}()
	go func() {
		for {
			select {
			case err := <-local_chan:
				exited_chan <- err
			case <-time.After(timeout):
				exited_chan <- ErrProcessTimeout
			}
		}
	}()

	return
}

func ServiceKey(config *ServiceConfig) string {
	return config.Name + "/" + config.ServiceVersion
}

func ServiceInstanceKey(config *ServiceInstanceConfig) string {
	return config.Name + "/" + config.ServiceVersion + "/" + config.Id
}
