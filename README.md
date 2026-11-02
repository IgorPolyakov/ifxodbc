odbc driver written in go. Implements database driver interface as used by standard database/sql package. It calls into odbc dll on Windows, and uses cgo (unixODBC) everywhere else.

To get started using odbc, have a look at the [wiki](https://github.com/alexbrainman/odbc/wiki) pages.

This code is based on two repositories:

- [alexbrainman/odbc](https://github.com/alexbrainman/odbc) - the upstream odbc driver; its full git history is preserved here
- [OpenInformix/IfxGo](https://github.com/OpenInformix/IfxGo) - an earlier fork with Informix-specific patches (Informix tests, removal of MSSQL/MySQL/FoxPro tests), applied on top
