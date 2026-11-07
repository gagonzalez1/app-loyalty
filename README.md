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

Requisitos: Go 1.26.10 o posterior, Docker y Docker Compose.

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

El builder y `go.mod` requieren Go 1.26.10. Los parches de seguridad de
`golang.org/x/crypto` requieren la serie Go 1.26; el cambio de herramienta
se valida con la suite completa, race, vet y govulncheck.

El runtime usa Alpine 3.21.8 y actualiza sus paquetes con `apk upgrade --no-cache`
antes de instalar los certificados. El healthcheck de `/v1/health/live` utiliza
`wget -qO-` de BusyBox; no se instala GNU wget ni su dependencia PCRE2.

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
| `EXPECTED_SCHEMA_VERSION` | Versión de esquema requerida por readiness; por defecto `0046`. |
| `MERCADO_PAGO_PROVIDER` | `api` habilita checkout y webhooks de suscripciones; `disabled` los mantiene apagados. |
| `MERCADO_PAGO_ACCESS_TOKEN`, `MERCADO_PAGO_WEBHOOK_SECRET` | Secretos del backend para crear suscripciones y validar notificaciones. Nunca se exponen al frontend. |
| `MERCADO_PAGO_BRANCH_PRICE_CENTS` | Precio mensual de Sellos por sucursal activa; por defecto `2500000` (ARS 25.000) para el piloto; configurable por entorno. |
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

El [flujo productivo con migraciones](docs/production-deployment.md) coordina
respaldo, restore/upgrade aislado, migración e identificación del release desde
la VPS. Su activación requiere instalar el controlador restringido y configurar
el secreto SSH de Actions antes de integrar el cambio de workflow.

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

## Preparación y activación de Mercado Pago

La interfaz y la API pueden desplegarse con `MERCADO_PAGO_PROVIDER=disabled`:
Plan y facturación permanece visible, pero checkout y cancelación están apagados.
La [plantilla productiva de facturación](deploy/mercadopago.production.env.example)
conserva ese estado y deja los secretos vacíos. Es un overlay, no una configuración
completa del backend.

Seguir la [guía de preparación y activación posterior](docs/mercadopago-production.md)
para seleccionar fuente vigente, conciliar migraciones, preparar el modo productivo,
confirmar precios y validar notificaciones antes de activar. El endpoint directo es
`https://api.puntazo.pro/v1/mercado-pago/webhooks`; los tópicos usados son
`subscription_preapproval`, `subscription_authorized_payment` y `payment`.
Con el proveedor habilitado, sólo se devuelve `200` después de procesar la entrega
(o ignorar un tópico con firma válida). Con `disabled`, el receptor devuelve `200`
sin consultar al proveedor ni persistir; esa respuesta no acredita una operación.

El propietario ve el total mensual antes de salir de Puntazo. Mercado Pago aloja
la captura del medio de pago; el frontend nunca recibe el access token ni datos de
tarjeta. `POST /v1/marcas/{brand_id}/suscripcion/cancelacion` envía el estado
`cancelled` al proveedor y detiene las renovaciones futuras.

La prueba gratuita de Sellos o Puntos termina un mes calendario después del
primer acceso verificado. El checkout conserva ese fin y no reinicia el período
al autorizar, cancelar o volver a contratar.

El checkout reserva la marca antes de llamar a Mercado Pago y sólo permite un
POST al proveedor por reserva. Si la respuesta se pierde, el estado queda en
`CREATING`: el webhook puede completarlo usando la referencia externa. Si no
llega, operaciones debe conciliar esa referencia con Mercado Pago antes de
resolver la reserva; repetir el POST automáticamente podría crear dos
suscripciones. Mientras el estado sea `CREATING`, `PENDING`, `AUTHORIZED` o
`PAUSED`, la API impide agregar o desactivar sucursales, cambiar el tipo de
programa o eliminar la marca. Primero debe cancelarse la suscripción.

## Referidos y Backoffice

Las migraciones `0023` y `0024` agregan campañas, influencers, códigos,
atribuciones únicas por marca, cobros verificados, recompensas y usuarios
internos. Las altas por email (`POST /v1/demo/comercios`) y Google
(`POST /v1/auth/google`, dentro de `merchant_registration`) aceptan
`referral_code` opcional. La API comprueba que el código y la campaña estén
activos y en ventana para el programa elegido. Copia los porcentajes y la
cantidad de cobros en la atribución antes de confirmar la marca. Pausar un
código o campaña sólo detiene atribuciones nuevas. Las marcas reciben un código
propio y sus propietarios/administradores pueden leerlo en
`GET /v1/marcas/{brand_id}/referidos/codigos`.

El Backoffice independiente usa `/v1/backoffice/*` y cuentas distintas de
clientes/comercios. `ADMIN_SISTEMA` crea campañas, influencers y códigos;
`FINANZAS` puede registrar la liquidación de una comisión en efectivo y una
compensación manual de crédito comercial con referencia externa. Todas
las mutaciones quedan en `backoffice_audit`. El acceso requiere email y contraseña
de una cuenta interna activa, con cookie HttpOnly SameSite=Strict de ocho horas. El cookie es
`Secure` cuando `APP_ENV` está definido y es distinto de `development`, incluido `staging`; el gateway debe
publicar esta web en HTTPS y reenviar `/v1/` al API en el mismo origen. Para
crear una cuenta interna tras migrar el esquema:

```bash
DATABASE_URL='postgresql://...' BACKOFFICE_EMAIL='persona@puntazo.pro' \
BACKOFFICE_PASSWORD='contraseña-larga' BACKOFFICE_ROLE='ADMIN_SISTEMA' \
go run ./cmd/backoffice-user
```

El comando crea una cuenta interna para ingresar con email y contraseña.
No reutilizar cuentas de la app. Los campos TOTP anteriores permanecen en la
base por compatibilidad y no se consultan durante el acceso.

El piloto Sellos se configura desde el Backoffice con una ventana de 90 días,
`discount_bps=5000`, `discount_charges=3`, `reward_bps=2000` y
`reward_charges=12`. El precio público por sucursal se toma del entorno al
abrir el checkout; `GET /suscripcion` muestra el precio promocional y el precio
completo. Sólo facturas recurrentes aprobadas y verificadas ante Mercado Pago
entran al registro. Al tercer cobro descontado se solicita el importe completo
para el siguiente cobro. La recompensa usa el importe efectivamente cobrado,
en centavos enteros. Los reembolsos anulan recompensas pendientes; si Finanzas
ya pagó una, pasa a `RECOVERY_DUE` para gestionar su recuperación.

Los créditos de marcas referentes quedan como saldo auditable en
`referral_merchant_credit_balances`. Finanzas puede registrar un reembolso
externo ya realizado contra una factura de suscripción pagada mediante
`POST /v1/backoffice/brands/{id}/credit-allocations`; la API verifica la
factura y el pago ante Mercado Pago, limita el importe al saldo disponible y
audita la referencia externa declarada por el operador. La API no verifica esa
transferencia externa ni descuenta automáticamente una próxima factura. Para
este último flujo falta comprobar en qué ciclo Mercado Pago aplica el cambio de
importe. Ver [liquidación manual de crédito](docs/referral-merchant-credit.md).

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

## Correo de bienvenida del influencer

El alta ADMIN_SISTEMA con `name/email/code/campaign_id` guarda el perfil, primer
código, auditoría y correo INFLUENCER_WELCOME en una única transacción.
Responde `email_queued=true`: confirma la cola, no la entrega. El payload anterior
`name/contact` sigue creando únicamente un perfil, sin correo. Los perfiles
existentes y los códigos adicionales no disparan un envío retroactivo.

La migración 0028 admite influencers sin cuenta de app en email_outbox, mantiene
la propiedad obligatoria de los correos de identidad y garantiza un welcome
por código. Copia las condiciones al alta bajo un lock compartido de campaña y
cifra el JSON con AES-GCM usando la clave de outbox existente. Las columnas
`token_*` transportan el payload cifrado sin crear un token de identidad.
El worker valida la relación influencer/código/destinatario y su lease; rechaza
un código desactivado. Borra el payload al completar o agotar cinco intentos.
La ventana de entrega es de siete días; no es la vigencia del código. La cola
conserva entrega al menos una vez: un fallo tras entregar y antes de confirmar
SENT puede duplicar un correo. El down bloquea si hay evidencia de welcome;
en ese caso corresponde una migración correctiva hacia adelante.

El HTML comparte plantilla de contraseña y personaje CID. Incluye nombre,
código, enlace PUBLIC_APP_URL con `?ref=...`, campaña, Sellos/Puntos/Sellos y
puntos, porcentajes y cantidad de cobros, fechas en Argentina y pausa al alta.
Explica que la comisión se calcula sobre el cobro aprobado y verificado, después
del descuento; no promete un importe fijo ni una transferencia automática.

**Operación:** SMTP usa el mismo remitente y proveedor de las contraseñas.
Configurar MAIL_PROVIDER=smtp, MAIL_FROM_ADDRESS, SMTP y una clave base64 de
32 bytes en OUTBOX_ENCRYPTION_KEY, incluso sin verificación de email requerida.
Sin clave, el alta nueva responde 503 EMAIL_UNAVAILABLE y revierte todo. Con
MAIL_PROVIDER=disabled y clave válida, los correos quedan pendientes.

Para desarrollo: APP_ENV=development, MAIL_PROVIDER=capture,
MAIL_CAPTURE_DIRECTORY=/ruta/absoluta/privada, remitente y clave válidos.
Capture escribe MIME .eml con permisos 0600 y no contacta destinatarios. SENT
en este modo significa guardado local. Conservar el directorio fuera de Git y
borrar sus mensajes al terminar las pruebas; la retención DB no elimina archivos
de captura. Capture se rechaza en otros entornos y con verificación requerida.



### Firma webhook Mercado Pago

La validación acepta timestamps Unix de 10 dígitos (segundos) y 13 dígitos
(milisegundos), con la misma ventana de frescura. El HMAC conserva el valor
original `ts`; no se normaliza el texto firmado.

### Audiencia Google para Android

`GOOGLE_ANDROID_WEB_CLIENT_ID` permite explícitamente el cliente OAuth web usado
por Android, además de `GOOGLE_CLIENT_ID`. Si está vacío se conserva una única
audiencia web; los tokens de otras audiencias y emails no verificados se rechazan.

### Registro con código de correo

`EMAIL_VERIFICATION_REQUIRED=true` exige verificar clientes y comercios antes de
emitir sesión. En testing se habilita inicialmente para probar SMTP y luego puede
cambiarse a `false` reiniciando el backend; sólo afecta altas nuevas. Producción
rechaza `false`. Google conserva su propia validación.

La app actual solicita `verification_method: code`. El código dura 10 minutos,
permite cinco intentos y se puede reenviar después de 60 segundos. El correo se
encola en la misma transacción que la cuenta y el secreto sólo vive cifrado en la
outbox hasta el envío. La huella se protege con HMAC y separación de dominio usando
`OUTBOX_ENCRYPTION_KEY`. El secreto temporal `registration_token` permite sesión
automática durante 30 minutos y sobrevive al reenvío; su huella es lo único guardado.
En comercios se deriva mediante HMAC de email y clave de idempotencia para poder
repetir la respuesta sin persistir el secreto. Confirmar sin este contexto verifica
el correo y exige login. Si emitir sesión falla tras verificar, la respuesta sigue
siendo `verified: true` sin sesión y permite login manual.

Los clientes anteriores que omiten `verification_method` reciben enlaces de 24h.
Solicitar un código rota la verificación pendiente e invalida enlaces anteriores;
la API de enlaces sigue disponible. Verificación, reset e invitaciones conservan
propósitos separados. Capture se permite sólo en desarrollo para pruebas locales.

Esquema requerido: `0046`. Los números 0036–0042 están reservados por historial de
testing; readiness valida el manifiesto de migraciones incluido en el binario,
permitiendo esos huecos sin aceptar migraciones faltantes o inesperadas.
