package daemon

import (
	"encoding/json"
	"fmt"
	"github.com/coreos/go-etcd/etcd"
	//"os"
	//"syscall"
	"testing"
	"time"
)

const (
	TEST_ServerId  = "test"
	TEST_KeyPrefix = "test_app"
)

var (
	params = &DaemonParameters{
		EtcdAddresses:       []string{"http://192.168.30.183:4001"},
		ServerId:            TEST_ServerId,
		EtcdKeyPrefix:       TEST_KeyPrefix,
		UpdateStateInterval: 3,
	}
)

func checkServerConfig(config1, config2 *ServerConfig) bool {
	if len(config1.ServiceInstances) != len(config2.ServiceInstances) {
		return false
	}
	for _, service1 := range config1.ServiceInstances {
		found := false
		for _, service2 := range config2.ServiceInstances {
			if service1 == service2 {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func Test_updateServerState(t *testing.T) {
	var err error
	var etcdResponse *etcd.Response
	var serverState ServerState
	var now, updateTime int64
	etcdClient := etcd.NewClient(params.EtcdAddresses)
	key := `/` + TEST_KeyPrefix + `/servers/` + TEST_ServerId + `/state`
	daemon := NewDaemon(params)
	go daemon.updateServerState()
	time.Sleep(time.Second)
	etcdResponse, err = etcdClient.Get(key, false, false)
	if err != nil {
		t.Fatal("etcd set err:", err.Error())
	}

	err = json.Unmarshal([]byte(etcdResponse.Node.Value), &serverState)
	if err != nil {
		t.Fatal("json.Unmarshal() err:", err.Error())
	}

	now = time.Now().Unix()
	updateTime = serverState.UpdateTime
	if updateTime > now || updateTime+2 < now {
		t.Errorf("Update time %d is not match now time %s\n", serverState.UpdateTime, now)
	}
	fmt.Println("Server stata:", serverState.State)
	now = time.Now().Unix()
	daemon.state.UpdateTime = now
	time.Sleep(5 * time.Second)
	etcdResponse, err = etcdClient.Get(key, false, false)
	if err != nil {
		t.Fatal("etcd set err:", err.Error())
	}

	err = json.Unmarshal([]byte(etcdResponse.Node.Value), &serverState)
	if err != nil {
		t.Fatal("json.Unmarshal() err:", err.Error())
	}

	if now != serverState.UpdateTime {
		t.Errorf("Update time unmatch!\nexpect: %d\ngot:    %d\n", now, serverState.UpdateTime)
	}

	daemon.SafeExit(3)
}

func Test_watchServerConfig(t *testing.T) {
	var err error
	var serverConfigString []byte
	var etcdResponse *etcd.Response
	var serverConfig2 ServerConfig

	etcdClient := etcd.NewClient(params.EtcdAddresses)
	// Set test config
	key := `/` + TEST_KeyPrefix + `/servers/` + TEST_ServerId + `/config/details`
	serverConfig := ServerConfig{
		ServiceInstances: []ServiceInstanceBriefConfig{
			ServiceInstanceBriefConfig{
				Name: "test_service",
				Id:   "t_1",
			},
		},
	}
	serverConfigString, err = json.Marshal(&serverConfig)
	if err != nil {
		t.Fatal("json.Marshal() err:", err.Error())
	}
	_, err = etcdClient.Set(key, string(serverConfigString), 0)
	if err != nil {
		t.Fatal("etcd set err:", err.Error())
	}
	daemon := NewDaemon(params)

	go daemon.watchServerConfig()
	etcdResponse = <-daemon.config_chan
	err = json.Unmarshal([]byte(etcdResponse.Node.Value), &serverConfig2)
	if err != nil {
		t.Fatal("json.Unmarshal() err:", err.Error())
	}

	if !checkServerConfig(&serverConfig, &serverConfig2) {
		t.Errorf("server config unmatch!\ngot:    %+v\nexpect: %+v\n", serverConfig, serverConfig2)
	}

	// Update config
	t.Log("Change server config\n")
	serverConfigString, err = json.Marshal(&serverConfig)
	if err != nil {
		t.Fatal("json.Marshal() err:", err.Error())
	}
	_, err = etcdClient.Set(key, string(serverConfigString), 0)
	if err != nil {
		t.Fatal("etcd set err:", err.Error())
	}
	etcdResponse = <-daemon.config_chan
	err = json.Unmarshal([]byte(etcdResponse.Node.Value), &serverConfig2)
	if err != nil {
		t.Fatal("json.Unmarshal() err:", err.Error())
	}

	if !checkServerConfig(&serverConfig, &serverConfig2) {
		t.Errorf("server config unmatch!\ngot:    %+v\nexpect: %+v\n", serverConfig, serverConfig2)
	}
	daemon.SafeExit(3)
}

func setServiceConfig(name, version string) (err error) {
	var serviceConfigString []byte
	etcdClient := etcd.NewClient(params.EtcdAddresses)
	key := `/` + TEST_KeyPrefix + `/services/` + name + `/versions/` + version + "/config/details"
	serviceConfig := ServiceConfig{
		Name:           name,
		ServiceVersion: version,
		DownloadScript: ServiceScript{"./tools/sleep", []string{"1"}, 2},
		InstallScript:  ServiceScript{"./tools/sleep", []string{"1"}, 2},
		RunScript:      ServiceScript{"./tools/sleep", []string{"100"}, 0},
	}
	serviceConfigString, err = json.Marshal(&serviceConfig)
	if err != nil {
		return
	}
	_, err = etcdClient.Set(key, string(serviceConfigString), 0)
	return
}
func setServiceInstanceConfig(name, version, id string) (err error) {
	var serviceConfigString []byte
	etcdClient := etcd.NewClient(params.EtcdAddresses)
	key := `/` + TEST_KeyPrefix + `/services/` + name + `/instances/` + id + "/config/details"
	serviceInstanceConfig := ServiceInstanceConfig{
		Name:           name,
		ServiceVersion: version,
		Id:             id,
	}
	serviceConfigString, err = json.Marshal(&serviceInstanceConfig)
	if err != nil {
		return
	}
	_, err = etcdClient.Set(key, string(serviceConfigString), 0)
	return
}

func Test_mainLoop(t *testing.T) {
	var err error
	//var etcdResponse *etcd.Response
	var serverConfigString []byte
	var key string
	serverId := "test_server"
	serviceVersion1 := "100.0"
	serviceVersion2 := "100.1"
	serviceName := "test_service"
	serviceInstanceId := "t_100"
	daemon := NewDaemon(params)

	// Set service configs
	err = setServiceConfig(serviceName, serviceVersion1)
	if err != nil {
		t.Fatalf("setServiceConfig(%s) err: %s\n", serviceName, err.Error())
	}
	err = setServiceConfig(serviceName, serviceVersion2)
	if err != nil {
		t.Fatalf("setServiceConfig(%s) err: %s\n", serviceName, err.Error())
	}

	// Set service instance config
	err = setServiceInstanceConfig(serviceName, serviceVersion1, serviceInstanceId)
	if err != nil {
		t.Fatalf("setServiceInstanceConfig(%s) err: %s\n", serviceName, err.Error())
	}

	// Prepare server config
	key = `/` + TEST_KeyPrefix + `/servers/` + serverId + "/config/details"
	serverConfig := ServerConfig{
		ServiceInstances: []ServiceInstanceBriefConfig{
			ServiceInstanceBriefConfig{
				Name: serviceName,
				Id:   serviceInstanceId,
			},
		},
	}
	serverConfigString, err = json.Marshal(&serverConfig)
	if err != nil {
		t.Fatal("json marshal err:", err.Error())
	}
	go daemon.mainLoop()
	daemon.config_chan <- &etcd.Response{
		Action: "Get",
		Node: &etcd.Node{
			Key:           key,
			Value:         string(serverConfigString),
			Dir:           false,
			ModifiedIndex: 100,
			CreatedIndex:  100,
		},
		EtcdIndex: 1,
		RaftIndex: 1,
	}
	time.Sleep(time.Second * 3)

	fmt.Println("change service instance config, version upgrade")
	// Set service instance config
	err = setServiceInstanceConfig(serviceName, serviceVersion2, serviceInstanceId)
	if err != nil {
		t.Fatalf("setServiceInstanceConfig(%s) err: %s\n", serviceName, err.Error())
	}

	serverConfigString, err = json.Marshal(&serverConfig)
	if err != nil {
		t.Fatal("json marshal err:", err.Error())
	}
	daemon.config_chan <- &etcd.Response{
		Action: "Get",
		Node: &etcd.Node{
			Key:           key,
			Value:         string(serverConfigString),
			Dir:           false,
			ModifiedIndex: 101,
			CreatedIndex:  101,
		},
		EtcdIndex: 2,
		RaftIndex: 2,
	}
	time.Sleep(time.Second * 3)

	daemon.SafeExit(3)
}

/*
func Test_RunProcess(t *testing.T) {
	var command string
	var argv []string
	var process *os.Process
	var err error
	exited_chan := make(chan error)
	command = `./tools/sleep`
	argv = []string{"100"}

	process, err = RunProcess(command, argv, exited_chan)
	if err != nil {
		t.Fatalf("RunProcess err: %s", err)
	}
	fmt.Printf("Process pid: %d\n", process.Pid)
	process.Signal(syscall.SIGINT)
	err = <-exited_chan
	if err != nil {
		t.Fatalf("Stop process err: %s\n", err.Error())
	}

	process, err = RunProcessWithTimeout(command, argv, exited_chan, 2*time.Second)
	if err != nil {
		t.Fatalf("RunProcess err: %s", err)
	}
	fmt.Printf("Process pid: %d\n", process.Pid)
	err = <-exited_chan
	if err != ErrProcessTimeout {
		t.Fatalf("Process should exit for timeout\n")
	}

}
*/
