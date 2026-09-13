# CPAMC `web-accounts` native plugin

Plugin nativo C ABI (ABI 1 / schema 6) para CLIProxyAPI/CPAMC. Expone un único
provider `web-accounts`; cada auth file declara `web_provider` y solo recibe
los modelos de su cuenta. Los IDs públicos llevan prefijo para no colisionar
con proveedores nativos (`web-claude/*`, `web-kimi/*`, `web-qoder/*`):

El campo obligatorio `GitHubRepository` del registro nativo enlaza al código
de protocolo de OmniRoute fijado en [este commit](https://github.com/diegosouzapw/OmniRoute/tree/152d95108c9c3d557562311ffed63240a511eb31/open-sse/executors).
Ese enlace documenta la procedencia de los adaptadores; este plugin nuevo no
está publicado ni mantenido por OmniRoute.

- `claude`: Claude Web mediante cookie `sessionKey` y descubrimiento de
  organización en `https://claude.ai/api/organizations`.
- `kimi`: Kimi Web internacional mediante `access_token` (también acepta el
  formato legado `kimi-auth`) y Connect-RPC en `https://www.kimi.ai`.
- `qoder`: Qoder CLI oficial con PAT en `QODER_PERSONAL_ACCESS_TOKEN`, para
  `qwen3.8-max-preview`. Es deliberadamente un transporte **buffered** (la CLI
  no ofrece token streaming en este adaptador), usa `--tools ""`, un directorio
  aislado y no hereda el entorno del proceso.

Qwen Web (`chat.qwen.ai`) **no está incluido**. La integración previa fue
retirada por su procedencia GPL derivada; no se revive ni se inventa un
protocolo. `examples/qwen-unavailable.json` documenta el estado; `examples/qoder.json`
contiene el formato de PAT para el conector Qoder. El modelo
Qwen3.8 disponible aquí es el conector Qoder explícito, no Qwen Web.

## Página en el menú de CPAMC

Desde la versión `0.1.2`, el registro nativo `management.register` declara un
recurso GET con el menú **Cuentas web**. CPAMC lo muestra automáticamente en
**PLUGINS** mediante su contrato público, sin cambios de fuente. La ruta
`/v0/resource/plugins/web-accounts/accounts` solo sirve una guía estática
incrustada en el binario: es pública, no tiene scripts ni formularios y nunca
recibe credenciales. Los botones de conexión están en la extensión de Chrome.

## Ingreso seguro de credenciales

La [extensión Chrome](../chrome-extension/README.md) puede añadir las cuentas
Claude/Kimi desde una sesión existente, o enviar un PAT de Qoder. Como
alternativa manual, CPAMC no necesita cambios de fuente: sube uno de los JSON de `examples/` por
el flujo existente de auth-file upload y habilita el plugin nativo. Define
`plugins.dir` como el directorio de descubrimiento del backend (por ejemplo
`/ruta/cli-proxy-api/plugins`; no es el directorio de auth salvo que tu
instalación lo configure así) y coloca allí el binario, o en su subdirectorio
de plataforma `darwin/arm64`, `linux/arm64`, `linux/amd64` o `windows/amd64`:

```text
/ruta/cli-proxy-api/plugins/darwin/arm64/web-accounts.dylib
/ruta/cli-proxy-api/plugins/linux/amd64/web-accounts.so
/ruta/cli-proxy-api/plugins/windows/amd64/web-accounts.dll
```

El loader prefiere el subdirectorio de plataforma y también acepta el binario
directamente bajo `plugins.dir`. Activa solo este plugin en la configuración
del backend, sin modificar CPAMC. La forma mínima es equivalente a:

```yaml
plugins:
  enabled: true
  dir: /ruta/cli-proxy-api/plugins
  configs:
    web-accounts:
      enabled: true
```

La ruta/configuración son opt-in del operador y no las escribe este proyecto. Nunca se
incluyen cookies o tokens en logs, labels, atributos, errores ni respuestas
visibles del plugin; el único transporte de secretos es `StorageJSON`,
administrado por el host. El plugin nativo no extrae datos del navegador; esa
función está aislada en la extensión opt-in. No se implementan bypass de
Cloudflare ni refresh especulativo.

Las credenciales solo se aceptan con `type: "web-accounts"` (el parser también
requiere `web_provider`). Se rechazan CR/LF. Claude admite un header Cookie,
un valor desnudo de `sessionKey`, o un array de objetos de cookies exportado por
DevTools. Kimi admite un objeto/localStorage con `access_token`, `Bearer ...`,
o `kimi-auth=...`. Qoder admite `personal_access_token`/`qoder_pat`.

## Alcance del chat

Se acepta únicamente chat de texto; no se declaran herramientas nativas ni
modalidades de imagen/audio/archivo. Para Qoder se requiere el binario oficial
`qodercli` o `qoder` resoluble en `PATH`; el plugin no lo instala ni acepta una ruta de
binario desde el auth JSON. La historia completa se serializa en cada
nueva conversación; no hay sesión persistente ni cache de conversaciones. Se
rechazan imágenes, audio, archivos, tool calls, tools/functions no vacíos y
parámetros desconocidos en lugar de descartarlos silenciosamente.

Claude y Kimi usan `host.http.do_stream` + `host.http.stream_read` y entregan
SSE OpenAI incremental mediante `host.stream.emit`; sus parsers rechazan frames
malformados/truncados y requieren `message_stop`/Connect EndStream. Qoder
ejecuta la CLI de forma acotada y entrega una secuencia OpenAI de chunks de rol,
contenido, fin y `[DONE]`; sigue siendo **buffered** (no token streaming) y esa
limitación está intencionalmente documentada.

## Construcción y uso local

Requiere Go 1.26, CGO y el loader nativo del backend. En una compilación nativa
el script ejecuta tests, vet y compila con `-trimpath`:

```sh
./build.sh
# macOS: web-accounts.dylib; Linux: web-accounts.so; Windows: web-accounts.dll
```

Para Linux se admiten `amd64` y `arm64`; macOS se limita a Apple Silicon
`arm64`; Windows admite `amd64`. Una compilación cruzada omite tests/vet (deben
ejecutarse en el runner nativo), requiere el compilador C del destino (o `$CC` / `zig cc`)
y escribe en una ruta separada para no reemplazar otro target:

```sh
GOOS=linux GOARCH=amd64 ./build.sh # dist/linux/amd64/web-accounts.so
GOOS=linux GOARCH=arm64 ./build.sh # dist/linux/arm64/web-accounts.so
GOOS=darwin GOARCH=arm64 ./build.sh # dist/darwin/arm64/web-accounts.dylib
GOOS=windows GOARCH=amd64 ./build.sh # dist/windows/amd64/web-accounts.dll
OUTPUT=/tmp/web-accounts.so GOOS=linux GOARCH=amd64 ./build.sh
```

`Dockerfile.build` es únicamente un builder Linux y no instala ningún
servicio. Por defecto usa Bookworm/glibc; para un builder Alpine/musl:

```sh
docker build -f Dockerfile.build --output type=local,dest=dist .
docker build --platform linux/arm64 -f Dockerfile.build \
  --output type=local,dest=dist-arm64 .
docker build -f Dockerfile.build \
  --build-arg BUILDER_IMAGE=golang:1.26-alpine \
  --output type=local,dest=dist-musl .
```

Los builds Docker producen Linux; los builds macOS deben ejecutarse en macOS
con el toolchain correspondiente. Para compilación cruzada sin toolchains
externos pesados, `build.sh` respeta `$CC` o recurre a `zig cc` (ej. `windows/amd64`
vía `x86_64-windows-gnu`, y `linux/amd64` vía `x86_64-linux-gnu.2.31` o
`x86_64-linux-musl` según `LIBC=glibc|musl`). Si no hay toolchain C disponible,
el build falla de forma segura (fail-closed).

El binario incluido solo puede utilizarse en macOS Apple Silicon; Linux y
Windows deben compilarse para la arquitectura del host desde fuente o CI. No instala ni
habilita el plugin, no modifica CPAMC/CLIProxyAPI y no realiza
requests contra cuentas reales durante la construcción. El directorio generado
`web-accounts.dylib`/`.so` y su header pueden eliminarse antes de empaquetar si
el artefacto se construye en CI.

No sobrescribas una biblioteca que el backend tenga cargada. Instala una nueva
versión en una ruta de target separada. En el backend 7.2.158 se ha validado
la recarga en caliente al cambiar realmente los bytes del config (por ejemplo,
un comentario de revisión), manteniendo sus valores. Una escritura idéntica
no dispara la recarga. Comprueba la versión de metadata y el estado efectivo;
la ruta descubierta en disco no basta. Reiniciar sigue siendo una alternativa
operativa, pero no se hace automáticamente.
En Unix el plugin retiene una referencia a su propia imagen para que el
runtime Go no se desmapee durante `dlclose`; esa memoria queda retenida hasta
el reinicio del proceso.

## Licencias

La adaptación de los formatos Claude Web (payload/SSE) y Kimi Web (Connect-RPC)
se basa en OmniRoute v3.8.51, MIT; ver `NOTICE`. Este plugin nuevo se distribuye
bajo MIT (`LICENSE`). La descarga/gestión de una credencial mediante las rutas
de management del host puede devolver el JSON en claro al operador autorizado;
esa es una propiedad del host, no del plugin.
