window.onload = function() {
  // Cairn: serve our own OpenAPI spec instead of petstore.
  window.ui = SwaggerUIBundle({
    url: "/api/docs/openapi.yaml",
    dom_id: '#swagger-ui',
    deepLinking: true,
    presets: [
      SwaggerUIBundle.presets.apis,
      SwaggerUIStandalonePreset
    ],
    plugins: [
      SwaggerUIBundle.plugins.DownloadUrl
    ],
    layout: "StandaloneLayout",
    // Disable the "Try it out" network calls when running behind a
    // reverse proxy on a different origin — relative requests still
    // work because SwaggerUIBundle uses the spec's servers field, but
    // the visual noise (a banner asking to confirm the CORS scheme)
    // is gone. Operators can re-enable it per-environment by editing
    // this file if they ever front cairn behind an HTTPS proxy.
    tryItOutEnabled: true,
    persistAuthorization: true
  });
};
