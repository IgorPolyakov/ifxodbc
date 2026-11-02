FROM golang:1.26

RUN apt-get update \
 && apt-get install -y --no-install-recommends unixodbc-dev \
 && rm -rf /var/lib/apt/lists/*

# Informix Client SDK (ODBC driver) taken from IBM's public sandbox image.
COPY --from=ibmcom/informix-developer-sandbox@sha256:678250715879a7cbdd2ea658ff7ecd7087dcaf0b8e39d47c936916edff62c5ec \
	/home/informix/odbc/ /opt/informix/

ENV ODBCINI=/etc/odbc.ini \
	INFORMIXDIR=/opt/informix \
	INFORMIXSQLHOSTS=/opt/informix/etc/sqlhosts \
	LD_LIBRARY_PATH=/opt/informix/lib:/opt/informix/lib/cli:/opt/informix/lib/esql

RUN mkdir -p /opt/informix/etc \
 && printf '[IBM INFORMIX ODBC DRIVER]\nDriver = /opt/informix/lib/cli/iclit09b.so\n' > /etc/odbcinst.ini \
 && printf '[ODBC]\nUNICODE=UCS-2\n' > /etc/odbc.ini

WORKDIR /src
