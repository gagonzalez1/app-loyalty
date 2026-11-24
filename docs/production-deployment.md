# Despliegue productivo con migraciones

El workflow `deploy-production.yml` delega el release a un controlador restringido
en la VPS. El controlador usa la API/UI oficial de Coolify; no modifica su base
interna ni sus archivos generados. PostgreSQL, sus credenciales, respaldos y clave
de cifrado permanecen en la VPS. La API no migra al arrancar.

## Secuencia

1. Actions ejecuta el CI reutilizable y verifica `origin/main`, HEAD y fuente limpia.
   Sólo el repositorio oficial `am-p/app-loyalty`, rama `main`, puede desplegar.
2. Una clave SSH con comando forzado solicita `deploy <SHA completo>`. El proceso
   de la VPS queda separado de la conexión y serializado con `flock`; una
   desconexión de Actions no interrumpe una migración. Actions no cancela un
   release activo por un push nuevo y consulta `status <SHA>` hasta su terminación.
3. El controlador vuelve a clonar/fetchear `am-p/app-loyalty:main`, exige HEAD
   vigente/limpio y un job `verify / verify` exitoso para ese commit. Construye la
   imagen candidata desde ese árbol antes de cambiar datos.
4. Consulta `schema_migrations` en la base productiva y comprueba secuencia,
   ausencia de huecos y hashes de las migraciones ya aplicadas. El primer ledger
   se obtiene de los SQL de la imagen realmente desplegada; los siguientes
   quedan protegidos en la VPS. No usa `/version` para adivinar el esquema real.
5. Si hay migraciones pendientes, exporta el diario acumulativo de bajas fuera
   de la base; genera un dump cifrado y verifica su descifrado por SHA256.
   Restaura en PostgreSQL16 aislado, sin red externa ni puertos publicados;
   aplica las migraciones dos veces y exige que el segundo pase no haga cambios.
   Comprueba readiness de la imagen candidata sobre esa copia, con correo,
   storage, push externo y cobros aislados/deshabilitados. Este ensayo no
   certifica los proveedores externos de producción.
6. Vuelve a fetch de main antes de la mutación productiva. Aplica el migrador de
   la imagen candidata, conserva el historial efectivamente aplicado incluso
   ante un fallo parcial y configura `EXPECTED_SCHEMA_VERSION` con la última
   versión del árbol. No hace falta editar un número fijo en el workflow.
7. Congela la revisión para este despliegue mediante la API de Coolify para
   evitar que un push concurrente cambie el código entre migración y publicación.
   Despliega por la API oficial, espera su estado terminal y vuelve el selector a
   `HEAD`. Finalmente exige el commit, esquema y readiness públicos correctos.

La versión anterior puede dar readiness503 durante una ampliación de esquema:
actualmente su verificación exige igualdad exacta. La construcción y el ensayo
ocurren antes de ese intervalo. Este flujo no promete despliegues sin interrupción.
Los hooks pre/post existentes pueden conservarse como pases idempotentes; no son
el mecanismo que introduce nuevas migraciones.

## Política para próximas migraciones

El bootstrap automatizado parte del esquema0035. Cada migración nueva debe
declarar en sus primeras diez líneas una línea exacta:

```sql
-- deploy: expand
```

CI comprueba esta declaración antes de integrar código nuevo. Los SQL0036–0043
preexistentes se reconocen mediante nombre y SHA-256 exactos, según la revisión
registrada abajo. No se añade el marcador a archivos ya aplicados. El workflow
conserva tanto los controles de producción como los disparadores de testing.
Esta revisión operativa no aprueba ni publica funcionalidades.

La revisión del PR debe comprobar que la ampliación conserva compatibilidad con
la versión anterior. El comentario registra esa decisión; no sustituye la revisión
del SQL ni demuestra compatibilidad por sí solo. Cambios contract/destructivos,
bases con otra numeración, downgrade o modificación de una migración aplicada
se detienen para una operación de mantenimiento específica. No renumerar versiones
aplicadas ni introducir migraciones ficticias para probar el flujo en producción.

## Instalación y activación

La instalación debe realizarla un administrador de la VPS desde una revisión
revisada de este repositorio. El controlador es código operativo con privilegios;
no se actualiza automáticamente desde ramas de PR ni acepta código enviado por SSH.

```sh
install -D -m 0755 ops/production_release.py /usr/local/lib/puntazo/production_release.py
install -d -m 0700 /var/lib/puntazo-production-release
install -m 0644 ops/puntazo-production-release-retention.cron /etc/cron.d/puntazo-production-release-retention
```

Crear una clave Ed25519 exclusiva para Actions. Agregar sólo su clave pública a
`/root/.ssh/authorized_keys`, preservando las entradas previas, con estas opciones:

```text
restrict,command="/usr/bin/python3 /usr/local/lib/puntazo/production_release.py" ssh-ed25519 <CLAVE_PUBLICA> puntazo-production-actions
```

La clave permite únicamente `deploy` y `status` con SHA hexadecimal de40 caracteres.
No permite shell, SCP/SFTP, terminal ni forwarding. `deploy` siempre vuelve a
verificar el main oficial y su CI; no despliega una rama o SHA histórico arbitrario.
El token administrativo del team Puntazo se lee únicamente en la VPS desde
`/root/.coolify-api-token-puntazo-root` (0600). El servidor necesita Docker,
Git, Python3, OpenSSL, PostgreSQL16 disponible como imagen y acceso a GitHub y a
los endpoints públicos de salud. Un fallo de acceso o de verificación bloquea el
release antes de migrar.

El controlador instalado debe coincidir byte a byte con el de main. Si un PR
cambia código del controlador, un administrador debe instalar esa revisión
revisada antes de activar su publicación; un controlador viejo falla cerrado.

En `am-p/app-loyalty`, una cuenta con permisos de administración de Actions debe
configurar **antes de integrar el PR**:

- Secret `PUNTAZO_PRODUCTION_SSH_KEY`: contenido de la clave privada exclusiva.
- Variable `PUNTAZO_PRODUCTION_SSH_HOST`: host/IP de la VPS comprobada.
- Verificar `ops/production_known_hosts` contra la clave del host obtenida por
  un canal ya confiable. El cliente usa `HostKeyAlias=puntazo-production` y
  `StrictHostKeyChecking=yes`; no usa `ssh-keyscan` como fuente de confianza.

No guardar claves privadas, tokens ni datos productivos en Git, logs de Actions
o artifacts. El secreto anterior `COOLIFY_DEPLOY_TOKEN` deja de ser usado por este
workflow; no revocar tokens compartidos por otras aplicaciones sin conciliar su uso.
El autodeploy directo de Coolify debe permanecer apagado para no eludir el
controlador. No iniciar despliegues manuales concurrentes en Coolify.

La cuenta de trabajo gagonzalez1 tiene acceso de lectura al repositorio oficial:
no puede cargar esos ajustes ni integrar el PR. Preparar el controlador en la VPS
no activa por sí solo el nuevo workflow ni publica una rama de desarrollo.

## Comprobación y recuperación

El dispatch manual usa `check_only=true` por defecto: ejecuta CI y omite el
despliegue. Para publicar main, seleccionar `check_only=false` o integrar un
push de aplicación en main. Nunca despachar una rama de desarrollo a producción.

Para un ensayo administrativo desde la VPS, sin migrar ni desplegar producción:

```sh
python3 /usr/local/lib/puntazo/production_release.py --rehearse <HEAD_REMOTO_VIGENTE>
```

Ese modo aún consulta fuente/CI y puede generar una copia cifrada; usa únicamente
la copia aislada para migraciones. La clave SSH de Actions no admite este comando.

Los resultados públicos del protocolo contienen sólo SHA, fase, estado, esquema
y UUID de despliegue. Logs, snapshots, envs y backups quedan privados bajo
`/var/lib/puntazo-production-release/releases/<SHA>-<intento>/` (directorios0700,
archivos0600). El ledger de hashes y el diario acumulativo de bajas están fuera de
los directorios de backup. El cron instalado elimina intentos terminales de más
de60 días, incluyendo sus copias, claves y envs; no sigue symlinks ni elimina
intentos aún activos, el ledger o el diario de bajas. `--prune` permite revisar el
plan sin borrar. Los intentos interrumpidos que carezcan de resultado terminal
requieren conciliación operativa antes de su limpieza.

Si falla un backup, restauración, ensayo o CI, no se inicia la migración productiva.
Si falla una migración parcialmente aplicada, no se ejecuta down ni restore
automático: el próximo intento verifica y conserva los hashes de las versiones
ya comprometidas y puede continuar una corrección compatible desde main vigente.
El esquema y la imagen anterior pueden requerir recuperación operativa mientras
se corrige el release; un error no se comunica como publicación exitosa.

Si se pierde la respuesta del POST de deploy o Coolify sigue activo tras el timeout,
se mantiene el SHA congelado y se informa `revision_frozen=true`. Antes de otro
release, revisar el UUID/historial por API/UI oficial, esperar su estado terminal
y restaurar `git_commit_sha=HEAD` sólo si esa operación ya no está activa.
No modificar la base interna de Coolify ni quitar un pin que aún usa otro release.

Una restauración de datos requiere detener ingress/workers y reaplicar en una
copia aislada el diario externo **más reciente**, no sólo el incluido en el dump.
El export previo a este backup no sustituye la exportación periódica ni la
conciliación de bajas posteriores al snapshot. No reactivar cuentas borradas.

Pruebas locales/CI:

```sh
python3 -m unittest discover -s ops -p 'test_production_release.py' -v
docker build --target api -t puntazo-api:ci .
python3 ops/test_production_release_integration.py --image puntazo-api:ci
```

La prueba de integración usa una base vacía aislada, genera la próxima versión
ficticia sin publicarla, comprueba backup/restore, idempotencia, readiness,
conservación de datos y rechazo de una migración fallida sin tocar producción.

## Reconciliación de migraciones existentes (20/10/2026)

Los SQL0036–0040 ya integrados a main y los0041–0043 propuestos para menús
y verificación ya existen en testing sin marcador de despliegue. Se revisaron
como ampliaciones: tablas e índices nuevos, columna nullable, catálogo de
plantillas ampliado y límite PDF ampliado. Se preservan byte a byte; el
controlador reconoce únicamente sus nombres y SHA-256 exactos mediante
`REVIEWED_EXPAND_MIGRATIONS`. Cualquier cambio de esos bytes aborta, aun si se
añade un marcador. Las migraciones nuevas siguen requiriendo
`-- deploy: expand`, ensayo y validación de historial. Esta clasificación no
aplica migraciones, no aprueba funcionalidades ni habilita producción.

Al instalar el controlador, copiar la versión revisada de este PR; una copia
anterior no conoce estos hashes. Los archivos aplicados siguen sujetos al
ledger de hashes y al ensayo con respaldo antes de cada migración real.

## Historial divergente de Apple en testing

La operación `reconcile-testing-apple-0045` es un mantenimiento explícito de
**testing**, separado del release productivo automático. Ver el procedimiento,
precondiciones y auditoría en [`migrations/README.md`](../migrations/README.md#0051--auditoría-y-reconciliación-de-apple-0045-en-testing).
El release productivo desde 0044 aplica normalmente 0045–0051; este comando no
se invoca desde Actions ni desde hooks de Coolify. No activar la API candidata
contra testing 0049 sin completar y verificar primero la reconciliación.
