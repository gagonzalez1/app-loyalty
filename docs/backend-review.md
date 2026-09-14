# Organización del backend después de la revisión de Ariel

Esta reorganización parte de `62963e7` y conserva el contrato HTTP, los claims
JWT de Sellos, las reglas de negocio y el SQL. No introduce una nueva migración.
Su presencia en una rama local no implica merge ni despliegue.

## Ubicación de responsabilidades

| Responsabilidad | Archivos |
|---|---|
| Arranque y cierre | `cmd/server/main.go` |
| Composición y protección de rutas | `cmd/server/router.go` |
| Registro de endpoints | `cmd/server/routes_{public,merchant,customer,movement,legacy}.go` |
| Configuración general y pool | `internal/config/config.go`, `internal/config/db.go` |
| Handlers por dominio | `internal/handler/{auth,customer,merchant,movement,health,legacy}.go` |
| Validación HTTP y errores | `internal/handler/{validation,errors}.go` |
| Políticas de límites | `internal/handler/rate_policy.go` |
| Servicios por dominio | `internal/service/{auth,customer,merchant,movement}.go` |
| QR, idempotencia y reintentos | `internal/service/{qr,idempotency,retry}.go` |
| Usuarios y alta comercial | `internal/repository/{user,merchant_registration}.go` |
| Consultas de comercios y tarjetas | `internal/repository/{merchant,cards}.go` |
| Lectura y escritura de movimientos | `internal/repository/{movement_read,movement}.go` |
| Persistencia de idempotencia y esquema | `internal/repository/{idempotency,schema}.go` |
| Trazabilidad, plazo, IP y recuperación | `internal/middleware/{request,timeout,client_ip,recovery}.go` |

Se mantienen los paquetes y los tipos compartidos `Service`, `Repository` y
`Handler`. Se separaron declaraciones; no se reemplazaron sus dependencias.
`Actor` conserva su nombre y ahora explica su alcance en un comentario.

## Configuración de PostgreSQL

API y migrador llaman a `ParsePoolConfig`. La política vuelve a los valores del
backend original: máximo 4 conexiones, mínimo 0, inactividad de 30 minutos,
vida máxima de una hora, health check cada minuto y conexión de 5 segundos.
No había mediciones que justificaran los valores 10/1/15 minutos del PR.
Este es el cambio operativo deliberado de la revisión. Un ajuste futuro debe
considerar capacidad de PostgreSQL, instancias y esperas del pool bajo carga.

`db.go` devuelve errores para que cada ejecutable decida cómo registrarlos y
terminar. No era necesario eliminar ese archivo para introducir `type Config`:
las funciones del pool tienen nombres diferentes y pueden convivir en el paquete.

## Autenticación y contexto

La petición entrega el token a `RequireAuth`, que verifica los claims y consulta
el estado actual de la cuenta. Guarda `Actor` con ID y tipo de cuenta en Gin.
El handler recupera esa identidad; las operaciones comprueban además membresías
y acceso a la marca/sucursal. Un tipo de cuenta no concede acceso global.

La validación Google recibe el contexto HTTP. La biblioteca puede descargar las
claves públicas cuando su caché debe renovarse; el contexto permite cancelar ese
trabajo. No modifica el contenido ni los permisos del token.

El primer acceso Google separa validación de identidad y creación. Una identidad
verificada que todavía no existe y omite `account_type` recibe
`422 ACCOUNT_TYPE_REQUIRED` y `details.next_action=SELECT_ACCOUNT_TYPE`; el
servicio no crea usuario ni sesión y, por integridad referencial, tampoco QR,
marca, membresía, sucursal o programa. El reenvío del mismo `id_token` como
`CLIENTE_FINAL` usa el alta de cliente, mientras que `PERSONAL_MARCA` exige los
datos comerciales y confirma todo el grafo en una transacción serializable.

La resolución de una cuenta existente ocurre antes de interpretar la selección:
su `tipo_cuenta` se conserva incluso si llegan `account_type` o datos comerciales
contradictorios. El alias `/auth/google` comparte el mismo servicio y mapeo de
errores que `/v1/auth/google`. No se persiste un estado pendiente ni se introduce
una migración para este flujo.

Los tokens del backend legacy usaban `sub` numérico y `rol`; Sellos usa `sub`
textual, `account_type`, emisor y audiencia con validaciones explícitas. Los
usuarios anteriores deben volver a iniciar sesión. Esta reorganización no vuelve
a cambiar los tokens Sellos ni su duración de 24 horas.

## Límites y recuperación

Las constantes en `rate_policy.go` conservan los umbrales anteriores. Los
contadores son por proceso y cuentan peticiones, no solamente fallos. Login v1 y
legacy comparten claves de IP/email; las operaciones de movimiento comparten
la clave del usuario. Reiniciar limpia los contadores; no hay estado compartido
entre réplicas. El exceso devuelve 429 y `Retry-After`.

El timeout propaga cancelación; no interrumpe código que ignore el contexto ni
escribe automáticamente un 504. `Recovery` captura panics del recorrido HTTP
en su goroutine. No reanuda la instrucción fallida, no revierte escrituras
confirmadas ni cubre goroutines independientes. Una respuesta ya iniciada no
siempre puede sustituirse por un 500. Los errores esperables se manejan con `error`.

## Transacciones y conflictos

El alta de cliente agrupa la creación y el hash final del QR. El alta demo agrupa
usuario, comercio y su contexto con el registro idempotente.

En la confirmación de movimientos, la transacción reclama la clave idempotente,
bloquea el preview, verifica identidad y vigencia, bloquea la tarjeta y después
el beneficio cuando corresponde. Se conserva ese orden para coordinar las
operaciones concurrentes. Saldo, historial, consumo del preview y resultado
idempotente se confirman juntos. Un error antes del commit ejecuta el rollback.

Repetir una clave con la misma operación y contenido recupera el resultado;
reutilizarla con otro contenido genera conflicto. Si el saldo cambió respecto
del preview, la confirmación se rechaza y se necesita un nuevo preview.
`retry.go` conserva hasta tres intentos solamente para errores PostgreSQL de
serialización (`40001`) y deadlock (`40P01`), respetando el contexto. No reintenta
indiscriminadamente errores de validación o falta de saldo.

## Migración y despliegue

`Dockerfile` y `cmd/migrate/main.go` son archivos agregados respecto de `f03b9aa`;
no fueron eliminados del PR. El README explica los targets y el paso de migración.

Esta versión usa una base nueva. `0001` no convierte automáticamente datos de
`users` a `usuarios`. Para conservar una base legacy hace falta definir y probar
un mapeo de datos; esa necesidad no se resuelve con una reorganización de archivos.
No ejecutar un cambio de instancia sobre datos legacy suponiendo compatibilidad.

`GIT_COMMIT` identifica la revisión declarada en `/v1/version`; debe cargarse con
el SHA construido. `EXPECTED_SCHEMA_VERSION` controla readiness, no ejecuta SQL.

## Verificación

`router_test.go` fija el inventario HTTP y comprueba 401 sin credenciales en todas
las rutas protegidas. Los tests existentes mantienen controles de permisos,
JSON, autenticación, idempotencia y concurrencia. `db_test.go` comprueba la
política compartida del pool y el retorno de errores de URL.

Los tests de QR, huellas y resolución de identidad se separaron en
`qr_test.go`, `idempotency_test.go` e `identity_test.go`. Las pruebas de lectura
comercial se encuentran en `merchant_read_test.go`; simulan peticiones, no un
login completo. La prueba `TestPostgresDemoSellosLifecycle` requiere
`TEST_DATABASE_URL` y debe ejecutarse explícitamente para evitar confundir un
skip con una integración validada.

El portal documental externo sigue fijado al main legacy del source lock. Esta
rama documenta aquí su organización; no se actualiza ese lock como si el PR
estuviera fusionado. Las URLs, flujos, esquema y OpenAPI de Sellos no cambiaron.

### Resultado de verificación — 7 de septiembre de 2026

- `go build ./...` y `go vet ./...`: correctos.
- `go test -race ./... -count=1` con `TEST_DATABASE_URL`: correcto.
- `TestPostgresDemoSellosLifecycle` ejecutado además con salida verbose: PASS,
  verificó persistencia de movimientos; no quedó omitido.
- Migrador ejecutado sobre una base vacía y repetido: aplicó `0001` una sola vez.
- Inventario de 23 rutas y autenticación de las 14 protegidas: correcto.
- Comparación de declaraciones antes/después: ninguna función perdida; 139 sin
  cambios de código. Las diferencias corresponden a composición del router,
  política del pool y sustitución de umbrales por constantes equivalentes.
- PostgreSQL 16 temporal local detenido después de las pruebas. No se usaron
  datos del despliegue. Docker no estaba activo; no se reconstruyeron imágenes.
- OpenAPI válido con 16 advertencias editoriales preexistentes: servidor local,
  descripciones de tags y resúmenes de algunas operaciones.

Esta verificación no ejecutó un despliegue.
