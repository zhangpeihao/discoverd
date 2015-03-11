# discoverd

Automated deployment and automated upgrade based on etcd.

## Usage (recommendation)

### Setup etcd

* Setup a single-member etcd test evironment

```
go get github.com/coreos/etcd
cd $GOPATH/src/github.com/coreos/etcd
go get ./...
go build
./etcd
```

* Or setup a cluster follow the guide on [multi-machine cluster](https://github.com/coreos/etcd/blob/master/Documentation/clustering.md)

### Configure your servers, services and service instances

#### Server configuration definition

We designed the configuration key of server as below:

```
<prefix>/servers/<server ID>/config/details
```

* prefix - We can set this prefix to avoid conflict with other system on etcd.
* server ID - We should assigned an ID to every server in our system.

The value stored on this key is formated in JSON.

```
{service_instances:[{name:string, id:string}]}
```

* service_instances - Defines all service instances should install and run on this server.
* service_instances.name - The service name.
* service_instances.id - The service instance ID. The discoverd program uses this ID to fetch the service instance configuration.


#### Service configuration definition

We designed the configuration key of service as below:

```
<prefix>/services/<service name>/versions/<version>/config/details
```

* prefix - We can set this prefix to avoid conflict with other system on etcd.
* service name - We can use service name to diffrentiate varientes of services.  For example, we have web services, mysql services and redis services.
* version - We can use diffrent configuration for diffrent version.

The value stored on this key is formated in JSON.

```
{
	name:string,
	download_script:{command:string, argv:[string], timeout:int},
	install_script:{command:string, argv:[string], timeout:int},
	run_script:{command:string, argv:[string]}
}
```

* name - The service name.
* download_script - The download script.  The discoverd program will execute this script first.
* install_script - The install script.  The discoverd program will execute this script after download script.
* run_script - The run script.  The discoverd program will execute this script after install script.
* script.command - The script command.  For example, we can use 'wget', 'git' or 'svn' commands to download the program or program install script.  If you don't want to run any scripts in download or install step, just leave this field empty.
* script.argv - We will uses those arguments to run the script.
* script.timeout - We set the timeout value(in second) to prevent the script dead block in some issues.


#### Service instance configuration definition

We designed the configuration key of service instance as below:

```
<prefix>/services/<service name>/instances/<ID>/config/details
```

* prefix - We can set this prefix to avoid conflict with other system on etcd.
* service name - We can use service name to diffrentiate varientes of services.  For example, we have web services, mysql services and redis services.
* ID - Every service instance has an unique ID in the same service name.

The value stored on this key is formated in JSON.

```
{
	name:string,
	service_version:string,
	run_script:{command:string, argv:[string]}
}
```

* name - The service name.
* service_version - Declare the version of this servive instance.
* run_script - We can define service instance run script to overwrite or improve the run script which defined in service configuration.

### Build

Build discoverd

```
go get github.com/zhangpeihao/discoverd
cd $GOPATH/src/github.com/zhangpeihao/discoverd
go get ./...
go build
```

### Deploy

#### Docker

#### Vagrant

#### VMware

