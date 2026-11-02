
DB_NAME=test
PASSWORD=Passw0rd

help:
	echo "use start or stop target"

# Microsoft SQL Server

MSSQL_CONTAINER_NAME=mssql_test
MSSQL_SA_PASSWORD=$(PASSWORD)
MSSQL_NETWORK=mssqlnetwork
MSSQL_DRIVER_NAME=ODBC Driver 17 for SQL Server

start-mssql:
	docker network create ${MSSQL_NETWORK}
	docker run \
		--name $(MSSQL_CONTAINER_NAME) \
		--hostname $(MSSQL_CONTAINER_NAME) \
		-e 'ACCEPT_EULA=Y' \
		-e 'MSSQL_SA_PASSWORD=$(MSSQL_SA_PASSWORD)' \
		-d \
		-p 1433:1433 \
		--network=${MSSQL_NETWORK} \
		mcr.microsoft.com/mssql/server:2022-latest
	echo -n "starting $(MSSQL_CONTAINER_NAME) "; \
		while ! \
			docker logs $(MSSQL_CONTAINER_NAME) 2>&1 | \
			grep SQL.Server.is.now.ready.for.client.connections >/dev/null ; \
		do echo -n .; sleep 2; done; echo " done"
	echo -n "creating database $(DB_NAME) "; \
		while ! \
			docker exec $(MSSQL_CONTAINER_NAME) \
				/opt/mssql-tools/bin/sqlcmd \
				-S localhost \
				-U SA \
				-P '$(MSSQL_SA_PASSWORD)' \
				-Q 'create database $(DB_NAME)' >/dev/null 2>&1 ; \
		do echo -n .; sleep 2; done; echo " done"

build-unixodbc:
	docker build \
		-t unixodbc \
		-f unixodbc.Dockerfile \
		.

test-mssql:
	docker run \
		-it \
		--network=${MSSQL_NETWORK} \
		-v .:/src \
		unixodbc \
			sh /src/mssqltest.sh \
				"$(MSSQL_DRIVER_NAME)" \
				"$(MSSQL_CONTAINER_NAME)" \
				"$(DB_NAME)" \
				"$(MSSQL_SA_PASSWORD)"

test-mssql-freetds:
	docker run \
		-it \
		--network=${MSSQL_NETWORK} \
		-v .:/src \
		unixodbc \
			sh /src/mssqltest.sh \
				"freetds" \
				"$(MSSQL_CONTAINER_NAME)" \
				"$(DB_NAME)" \
				"$(MSSQL_SA_PASSWORD)"


test-mssql-race:
	docker run \
		-it \
		--network=${MSSQL_NETWORK} \
		-v .:/src \
		unixodbc \
			sh /src/mssqltest.sh \
				"$(MSSQL_DRIVER_NAME)" \
				"$(MSSQL_CONTAINER_NAME)" \
				"$(DB_NAME)" \
				"$(MSSQL_SA_PASSWORD)" \
				"--race"

stop-mssql:
	docker stop $(MSSQL_CONTAINER_NAME)
	docker rm $(MSSQL_CONTAINER_NAME)
	docker network rm ${MSSQL_NETWORK}

# MySQL

MYSQL_CONTAINER_NAME=mysql_test
MYSQL_ROOT_PASSWORD=$(PASSWORD)

start-mysql:
	docker run --name=$(MYSQL_CONTAINER_NAME) -e 'MYSQL_ROOT_PASSWORD=$(MYSQL_ROOT_PASSWORD)' -d -p 127.0.0.1:3306:3306 mysql
	echo -n "starting $(MYSQL_CONTAINER_NAME) "; while ! docker logs $(MYSQL_CONTAINER_NAME) 2>&1 | grep ^Version.*port:.3306 >/dev/null ; do echo -n .; sleep 1; done; echo " done"
	docker exec $(MYSQL_CONTAINER_NAME) sh -c 'echo "create database $(DB_NAME)" | MYSQL_PWD=$(MYSQL_ROOT_PASSWORD) mysql -hlocalhost -uroot'

test-mysql:
	go test -v -mydb=$(DB_NAME) -mypass=$(MYSQL_ROOT_PASSWORD) -mysrv=127.0.0.1 -myuser=root -run=MYSQL

stop-mysql:
	docker stop $(MYSQL_CONTAINER_NAME)
	docker rm $(MYSQL_CONTAINER_NAME)

# Informix

IFX_CONTAINER_NAME=informix_test
IFX_NETWORK=informixnetwork
IFX_IMAGE=ibmcom/informix-developer-database:14.10.FC3DE
IFX_DB_NAME=test
IFX_DB_USER=test
IFX_DB_PASS=test

start-ifx:
	docker network create ${IFX_NETWORK}
	docker run \
		--name $(IFX_CONTAINER_NAME) \
		--hostname $(IFX_CONTAINER_NAME) \
		--tty \
		--privileged \
		-e LICENSE=accept \
		-e DB_NAME=$(IFX_DB_NAME) \
		-e DB_USER=$(IFX_DB_USER) \
		-e DB_PASS=$(IFX_DB_PASS) \
		-d \
		--network=${IFX_NETWORK} \
		$(IFX_IMAGE)
	echo -n "starting $(IFX_CONTAINER_NAME) (first boot takes a few minutes)"; \
		while ! echo 'select count(*) from sysmaster:sysdual;' | \
			docker exec -i $(IFX_CONTAINER_NAME) dbaccess $(IFX_DB_NAME) >/dev/null 2>&1 ; \
		do echo -n .; sleep 5; done; echo " done"

build-ifx:
	docker build \
		-t ifxodbc \
		-f informix.Dockerfile \
		.

test-ifx: IFXSERVER=$(shell docker exec $(IFX_CONTAINER_NAME) printenv INFORMIXSERVER)
test-ifx:
	echo "$(IFXSERVER) onsoctcp $(IFX_CONTAINER_NAME) 9088" > /tmp/sqlhosts.ifx
	docker run \
		--rm \
		--network=${IFX_NETWORK} \
		-e INFORMIXSERVER=$(IFXSERVER) \
		-v $(CURDIR):/src \
		-v /tmp/sqlhosts.ifx:/opt/informix/etc/sqlhosts:ro \
		ifxodbc \
			go test -v -run 'IFX' \
				-ifxsrv=$(IFXSERVER) \
				-ifxdb=$(IFX_DB_NAME) \
				-ifxuser=$(IFX_DB_USER) \
				-ifxpass=$(IFX_DB_PASS)

stop-ifx:
	docker stop $(IFX_CONTAINER_NAME)
	docker rm $(IFX_CONTAINER_NAME)
	docker network rm $(IFX_NETWORK)

