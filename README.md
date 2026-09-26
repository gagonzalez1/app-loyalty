# Puntazo · backend Sellos

API del lanzamiento gratuito de Puntazo para programas `SELLOS` y `PUNTOS`, implementada con Go, Gin, pgx y PostgreSQL 16. El contrato versionado está en [`openapi.yaml`](./openapi.yaml) y el proceso de la API no crea ni modifica tablas al iniciar.

Esta versión parte de una base PostgreSQL nueva. No migra ni reutiliza cuentas de la tabla legacy `users`.

La organización del código y las aclaraciones de la revisión de Ariel están en
[`docs/backend-review.md`](./docs/backend-review.md). Los endpoints y las reglas
de Sellos se conservan; el pool recupera el presupuesto original de 4 conexiones
máximas, 0 mínimas y 30 minutos de inactividad, compartido por API y migrador.

Los JWT del backend legacy no son compatibles con los claims y validaciones de
esta versión: al pasar a Sellos se requiere iniciar sesión nuevamente. La vigencia
sigue siendo 24 horas; los aliases HTTP legacy no convierten tokens anteriores.

## Desarrollo local

Requisitos: Go 1.25.13, Docker y Docker Compose.

Para levantar PostgreSQL, aplicar las migraciones y arrancar la API con las mismas imágenes usadas en despliegue:

```bash
cp .env.example .env
# Reemplazar JWT_SECRET, QR_PEPPER y la contraseña de PostgreSQL.
docker compose --env-file .env up --build
```

La API queda disponible en `http://localhost:8080/v1`. Los aliases `/auth/register`, `/auth/login`, `/auth/google` y `/me` se mantienen sólo para compatibilidad temporal.

Para ejecutar Go directamente desde el host, cambiar el host de `DATABASE_URL` de `db` a `localhost` y usar:

```bash
go run ./cmd/migrate up
go run ./cmd/server
```

## Imágenes independientes

El [`Dockerfile`](./Dockerfile) contiene dos targets:

- `api`: binario HTTP y migrador disponibles, sin ejecutar migraciones automáticamente al iniciar;
- `migrate`: migrador con los SQL versionados incluidos y `MIGRATIONS_DIR=/migrations`.

```bash
docker build --target migrate -t puntazo-migrate .
docker build --target api -t puntazo-api .
docker run --rm --env-file .env puntazo-migrate up
docker run --rm --env-file .env -p 8080:8080 puntazo-api
```

En un despliegue se debe ejecutar la migración `up` y sólo después publicar la API. El target `api` también contiene el migrador para que una plataforma pueda usar este comando previo al despliegue sin construir otra imagen:

```bash
docker run --rm --env-file .env \
  --entrypoint /usr/local/bin/puntazo-migrate puntazo-api up
```

Las migraciones toman un advisory lock, registran `schema_migrations` y son idempotentes. `/v1/health/ready` exige que la versión aplicada coincida con `EXPECTED_SCHEMA_VERSION`.

Las migraciones descendentes están deshabilitadas por defecto:

```bash
ALLOW_MIGRATION_DOWN=true go run ./cmd/migrate down
```

## Configuración

| Variable | Uso |
|---|---|
| `APP_ENV` | Usar `production` en producción; exige una allowlist CORS HTTPS explícita. |
| `DATABASE_URL` | PostgreSQL nuevo y exclusivo para esta API. |
| `JWT_SECRET` | Secreto aleatorio de al menos 32 bytes. |
| `JWT_ISSUER` | Issuer firmado y validado en JWT; por defecto `puntazo`. Cambiarlo invalida sesiones previas. |
| `QR_PEPPER` | Secreto distinto de `JWT_SECRET`, de al menos 32 bytes. |
| `DEMO_SIGNUP_ENABLED` | Habilita o cierra nuevas altas gratuitas sin bloquear cuentas existentes. |
| `CORS_ORIGINS` | Orígenes web exactos permitidos, separados por comas; habilita credenciales para la cookie HttpOnly de refresh. |
| `GOOGLE_CLIENT_ID` | Audiencia web de Google; opcional para el alias legado. |
| `EXPECTED_SCHEMA_VERSION` | Versión de esquema requerida por readiness; por defecto `0021`. |
| `MERCADO_PAGO_PROVIDER` | `api` habilita checkout y webhooks de suscripciones; `disabled` los mantiene apagados. |
| `MERCADO_PAGO_ACCESS_TOKEN`, `MERCADO_PAGO_WEBHOOK_SECRET` | Secretos del backend para crear suscripciones y validar notificaciones. Nunca se exponen al frontend. |
| `MERCADO_PAGO_BRANCH_PRICE_CENTS` | Precio mensual de Sellos por sucursal activa; por defecto `1500000` (ARS 15.000). |
| `MERCADO_PAGO_POINTS_BRANCH_PRICE_CENTS` | Precio mensual de Puntos por sucursal activa; por defecto `2000000` (ARS 20.000). |
| `APP_VERSION` | Etiqueta de versión informada por `/v1/version`; por defecto `dev`. |
| `GIT_COMMIT` | Revisión del código informada por `/v1/version`; `0000000` si no se proporciona. No ejecuta Git. |
| `TRUSTED_PROXY_COUNT` | Cantidad de proxies confiables para resolver la IP usada por rate limits. |
| `MEDIA_PROVIDER` | `s3` habilita imágenes privadas; es obligatorio en producción. |
| `S3_ENDPOINT` | Endpoint interno del storage S3-compatible para readiness, uploads y borrados. Debe usar HTTPS en producción. |
| `S3_PUBLIC_ENDPOINT` | Endpoint público opcional usado sólo para presigned GET; por defecto usa `S3_ENDPOINT`. En producción debe ser HTTPS y no incluir credenciales, query ni fragmento. |
| `S3_REGION`, `S3_BUCKET` | Región y bucket S3-compatible privado. |
| `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY` | Credenciales de mínimo privilegio para el bucket privado. |
| `S3_SERVER_SIDE_ENCRYPTION` | `AES256` por defecto y obligatorio en producción; se envía en cada upload. |
| `MEDIA_UPLOAD_GLOBAL_CONCURRENCY` | Máximo de uploads procesados simultáneamente por instancia; por defecto `8`. |
| `MEDIA_UPLOAD_ACTOR_CONCURRENCY` | Máximo simultáneo por actor; por defecto `2` y nunca mayor al global. |

Los secretos se configuran únicamente en el runtime. Nunca deben copiarse al frontend ni a variables `EXPO_PUBLIC_*`.

El despliegue debe proporcionar el SHA real de la revisión construida en
`GIT_COMMIT` y el esquema correspondiente en `EXPECTED_SCHEMA_VERSION`.
La API informa esa etiqueta; no verifica por sí misma el contenido del binario.
Configurar la versión de esquema no aplica migraciones.

## Notificaciones de tarjetas

La app Expo registra su destino con `POST /v1/clientes/me/push-token` usando el
access token de un `CLIENTE_FINAL` y un cuerpo como
`{"expo_push_token":"ExpoPushToken[ejemplo]","device_id":"telefono-principal"}`.
`device_id` es opcional; se recomienda un identificador estable por instalación
para reemplazar el token al rotar. Se admiten varios dispositivos por cliente.
El endpoint responde `200` con `data.registered: true`.

La migración `0021` agrega tokens y una cola persistida. Un trigger sobre
`tarjetas` crea trabajos dentro de la misma transacción al insertar, actualizar
o eliminar una tarjeta. También se encolan avisos si cambian marca, programa,
beneficios o imágenes que aparecen en la respuesta de tarjetas. El worker envía
el aviso a Expo fuera de la petición,
reintenta fallos transitorios hasta cinco veces y elimina tokens rechazados como
`DeviceNotRegistered`. La API de Expo acepta el envío de forma asíncrona; una
respuesta satisfactoria significa que Expo lo aceptó, no que el dispositivo lo
mostró. La app debe actualizar `GET /v1/clientes/me/tarjetas?page=1&page_size=100`
al recibir `data.action = REFRESH_CARDS`.

## Activación de Mercado Pago

La interfaz y la API pueden desplegarse con `MERCADO_PAGO_PROVIDER=disabled`: la
sección Plan y facturación permanece visible y explica que el proveedor aún no está
configurado, pero no permite iniciar ni cancelar cobros. Para habilitarla:

1. Aplicar las migraciones hasta `0021` y mantener `EXPECTED_SCHEMA_VERSION=0021`.
2. Cargar únicamente en el runtime del backend `MERCADO_PAGO_ACCESS_TOKEN` y
   `MERCADO_PAGO_WEBHOOK_SECRET`; no usar variables `EXPO_PUBLIC_*`.
3. Cambiar `MERCADO_PAGO_PROVIDER=api` y reiniciar la API.
4. Registrar en Mercado Pago la URL pública
   `https://<host>/api/v1/mercado-pago/webhooks` para el evento
   `subscription_preapproval`.
5. Ejecutar un alta, retorno, webhook y cancelación completos con credenciales de
   prueba antes de usar credenciales productivas.

El propietario ve el total mensual antes de salir de Puntazo. Mercado Pago aloja
la captura del medio de pago; el frontend nunca recibe el access token ni datos de
tarjeta. `POST /v1/marcas/{brand_id}/suscripcion/cancelacion` envía el estado
`canceled` al proveedor y detiene las renovaciones futuras.

El primer checkout de una marca Sellos o Puntos incluye una prueba gratuita de un mes.
La elegibilidad es de una sola vez: si esa marca cancela y vuelve a contratar,
el nuevo checkout comienza con la facturación mensual normal.

El checkout reserva la marca antes de llamar a Mercado Pago y sólo permite un
POST al proveedor por reserva. Si la respuesta se pierde, el estado queda en
`CREATING`: el webhook puede completarlo usando la referencia externa. Si no
llega, operaciones debe conciliar esa referencia con Mercado Pago antes de
resolver la reserva; repetir el POST automáticamente podría crear dos
suscripciones. Mientras el estado sea `CREATING`, `PENDING`, `AUTHORIZED` o
`PAUSED`, la API impide agregar o desactivar sucursales, cambiar el tipo de
programa o eliminar la marca. Primero debe cancelarse la suscripción.

## Primer acceso con Google

`POST /v1/auth/google` y el alias temporal `POST /auth/google` validan primero el
`id_token`. Si la identidad corresponde a una cuenta existente, emiten la sesión
con el `tipo_cuenta` persistido; cualquier `account_type` o
`merchant_registration` recibido se ignora y no puede convertirla.

Para una identidad Google verificada y nueva, una primera petición que envía sólo
`id_token` responde `422 ACCOUNT_TYPE_REQUIRED` con
`details.next_action=SELECT_ACCOUNT_TYPE`. Esa respuesta no crea usuario, sesión,
QR, marca, membresía, sucursal ni programa. La API tampoco entrega un token
intermedio: el frontend conserva el mismo ID token únicamente en memoria y lo
reenvía con una de estas variantes:

- `account_type=CLIENTE_FINAL`, sin `merchant_registration`, crea el cliente y su QR;
- `account_type=PERSONAL_MARCA`, junto con un `merchant_registration` válido, crea
  atómicamente usuario, marca, propietario, sucursal, programa y acceso demo.

Un ID token inválido o vencido responde `401 UNAUTHENTICATED`; el cliente debe
reiniciar el acceso con Google. Las altas cerradas y el código comercial inválido
fallan antes de confirmar datos parciales. Este flujo usa las rutas y tablas
existentes y no requiere una migración.

## Verificación

```bash
gofmt -w ./cmd ./internal
go vet ./...
go test -race ./...
npx --yes @redocly/cli@1.34.5 lint openapi.yaml
TEST_DATABASE_URL='postgresql://...' \
  go test ./internal/repository -run TestPostgresDemoSellosLifecycle -v
```

`TEST_DATABASE_URL` debe apuntar a una instancia de pruebas. El harness crea un schema aleatorio, ejecuta la migración y lo elimina al finalizar.

## Seguridad operativa

- PostgreSQL conserva únicamente `SHA-256(pepper || qr_token)`; el token QR se deriva mediante HMAC para restaurarlo al propietario sin persistirlo.
- JSON se limita a 1 MiB y los logs estructurados no registran bodies, JWT, QR ni secretos.
- Las confirmaciones requieren `Idempotency-Key`, transacción serializable, `SELECT FOR UPDATE` y hasta tres reintentos para `40001`/`40P01`.
- Para frontend y backend en orígenes distintos, `CORS_ORIGINS` debe contener el origen HTTPS exacto del frontend.
- Las imágenes aceptan JPEG, PNG y WebP por contenido real; se reencodean a JPEG/PNG, se limitan a 5 MiB y se reducen a 1024 px para logos o 512 px para iconos. El worker reconcilia uploads interrumpidos y borrados mediante leases persistidos.
- Readiness comprueba PostgreSQL, versión de esquema, Redis y acceso al bucket privado cuando media está habilitado. Antes de abrir tráfico se debe verificar además un upload/listado/borrado real con credenciales de staging.
