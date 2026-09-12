# CPAMC Web Accounts

Plugin nativo para el backend CLIProxyAPI administrado desde CPAMC, sin modificar
ninguno de sus códigos fuente. Adaptadores de chat de texto para Claude Web,
Kimi Web y Qoder/Qwen3.8. **Qwen Web no está implementado.**

- macOS: Apple Silicon (M), `darwin/arm64` únicamente. Intel no soportado.
- Linux/Docker: `amd64` y `arm64`; elegir glibc o musl según el contenedor.
- El estado de las pruebas locales no equivale a validación con cuentas reales.

## Compilar

```sh
cd web-accounts
./build.sh
```

[Instalación, credenciales y limitaciones](web-accounts/README.md).
[Pruebas de integración](integration/README.md).

Los repositorios upstream descargados, binarios, toolchains, credenciales y
registros locales quedan fuera de Git. No se incluyen cookies ni tokens reales.
