# CPAMC Web Accounts

Plugin nativo para el backend CLIProxyAPI administrado desde CPAMC, sin modificar
ninguno de sus códigos fuente. Adaptadores de chat de texto para Claude Web,
Kimi Web y Qoder/Qwen3.8. **Qwen Web no está implementado.**

- macOS: Apple Silicon (M), `darwin/arm64` únicamente. Intel no soportado.
- Linux/Docker: `amd64` y `arm64`; elegir glibc o musl según el contenedor.
- Windows: `amd64`.
- El estado de las pruebas locales no equivale a validación con cuentas reales.

## Instalación rápida

1. Descargá tu binario desde la
   [release v0.2.0](https://github.com/commonriskpro/cpamc-web-accounts/releases/tag/v0.2.0):
   - Windows: `web-accounts-windows-amd64.dll` → renombralo a `web-accounts.dll`
   - Linux glibc (Debian/Ubuntu): `web-accounts-linux-amd64.so`
   - Linux musl (Alpine): `web-accounts-linux-amd64-musl.so` → renombralo a `web-accounts.so`
   - macOS M: `web-accounts-darwin-arm64.dylib`
2. Copialo al subdirectorio de plataforma dentro del `plugins.dir` del backend:
   `windows/amd64/`, `linux/amd64`, `linux/arm64` o `darwin/arm64`.
3. Habilitá el plugin en el YAML del backend:
   ```yaml
   plugins:
     enabled: true
     dir: /ruta/cli-proxy-api/plugins
     configs:
       web-accounts:
         enabled: true
   ```
4. Si el backend ya corre, cambiá un byte real del config (ej. un comentario)
   para la recarga en caliente y verificá `metadata.version` en la UI.
   Nunca sobrescribas el binario que está cargado.

[Detalle completo de construcción y Docker](web-accounts/README.md).

El plugin nativo registra **Cuentas web** en la sección **PLUGINS** del menú
lateral de CPAMC. Esta página es una guía pública; las acciones de conexión
y las credenciales se mantienen en la extensión de Chrome, no en el iframe.

## Conectar desde Chrome

La [extensión local](chrome-extension/README.md) abre un panel dentro de CPAMC,
sin modificar su fuente ni pedir de nuevo la clave de Management:

1. Carga `chrome-extension/` como extensión descomprimida.
2. Abre CPAMC en `/management.html` y pulsa el icono de la extensión.
3. Pulsa **Usar sesión de CPAMC** y luego **Refresh All** en CPAMC.
4. Elige **Conectar Claude/Kimi**, o introduce un PAT de Qoder.

El panel y la API deben compartir origen. Para un servidor remoto se requiere
HTTPS; HTTP se limita a loopback. Docker no necesita acceso a tu navegador:
la extensión vive en el equipo donde abres CPAMC y solo captura el proveedor
que eliges. No se leen formularios ni almacenamiento persistente de CPAMC.

Las pruebas usan credenciales ficticias; la instalación en Chrome y la
inferencia con cuentas reales son verificaciones distintas todavía pendientes.

## Compilar

```sh
cd web-accounts
./build.sh
```

[Instalación, credenciales y limitaciones](web-accounts/README.md).
[Pruebas de integración](integration/README.md).

Los repositorios upstream descargados, binarios, toolchains, credenciales y
registros locales quedan fuera de Git. No se incluyen cookies ni tokens reales.
