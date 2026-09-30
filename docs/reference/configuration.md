# Configuration Reference

> Generated from code. Do not edit manually.

| Variable | Type | Required | Secret | Default | Description |
|---|---|---|---|---|---|
| `APP_ENV` | string | false | false | `development` | Runtime environment name. |
| `DATABASE_URL` | string | true | true | `` | PostgreSQL connection URL. |
| `HTTP_ADDR` | string | false | false | `:8080` | HTTP listen address for turaco-api. |
| `LOG_LEVEL` | string | false | false | `info` | Application log level. |
| `S3_ACCESS_KEY_ID` | string | false | true | `` | S3 access key when required. |
| `S3_BUCKET` | string | false | false | `turaco-dev` | Object-storage bucket/namespace. |
| `S3_ENDPOINT` | string | false | false | `` | S3-compatible endpoint; set for non-AWS/local providers. |
| `S3_PATH_STYLE` | bool | false | false | `true` | Use path-style S3 addressing. |
| `S3_REGION` | string | false | false | `us-east-1` | S3 region. |
| `S3_SECRET_ACCESS_KEY` | string | false | true | `` | S3 secret key when required. |
