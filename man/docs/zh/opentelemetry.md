{{.CSS}}
# OpenTelemetry
---

{{.AvailableArchs}}

---

OpenTelemetry （以下简称 OTEL）是 CNCF 的一个可观测性项目，旨在提供可观测性领域的标准化方案，解决观测数据的数据模型、采集、处理、导出等的标准化问题。

OTEL 是一组标准和工具的集合，旨在管理观测类数据，如 trace、metrics、logs 等 (未来可能有新的观测类数据类型出现)。

OTEL 提供与 vendor 无关的实现，根据用户的需要将观测类数据导出到不同的后端，如开源的 Prometheus、Jaeger、Datakit 或云厂商的服务中。

本篇旨在介绍如何在 Datakit 上配置并开启 OTEL 的数据接入，以及 Java、Go 的最佳实践。

***版本说明***：Datakit 目前只接入 OTEL v1 版本的 otlp 数据。

## 配置说明 {#config}

=== "主机安装"

    进入 DataKit 安装目录下的 `conf.d/{{.Catalog}}` 目录，复制 `{{.InputName}}.conf.sample` 并命名为 `{{.InputName}}.conf`。示例如下：
    
    ```toml
    {{ CodeBlock .InputSample 4 }}
    ```

    配置好后，[重启 DataKit](datakit-service-how-to.md#manage-service) 即可。

=== "Kubernetes"

    目前可以通过 [ConfigMap 方式注入采集器配置](datakit-daemonset-deploy.md#configmap-setting)来开启采集器。

### 注意事项 {#attentions}

1. 建议使用 grpc 协议, grpc 具有压缩率高、序列化快、效率更高等优点。

1. http 协议的路由是不可配置的，请求路径是 trace:`/otel/v1/trace` ，metric:`/otel/v1/metric`

1. 在涉及到 `float` `double` 类型数据时，会最多保留两位小数。

1. http 和 grpc 都支持 gzip 压缩格式。在 exporter 中可配置环境变量来开启：`OTEL_EXPORTER_OTLP_COMPRESSION = gzip`, 默认是不会开启 gzip。
    
1. http 协议请求格式同时支持 json 和 protobuf 两种序列化格式。但 grpc 仅支持 protobuf 一种。

1. 配置字段 `ignore_attribute_keys` 是过滤掉一些不需要的 Key 。但是在 OTEL 中的 `attributes` 大多数的标签中用 `.` 分隔。例如在 resource 的源码中：

```golang
ServiceNameKey = attribute.Key("service.name")
ServiceNamespaceKey = attribute.Key("service.namespace")
TelemetrySDKNameKey = attribute.Key("telemetry.sdk.name")
TelemetrySDKLanguageKey = attribute.Key("telemetry.sdk.language")
OSTypeKey = attribute.Key("os.type")
OSDescriptionKey = attribute.Key("os.description")
...
```

因此，如果您想要过滤所有 `teletemetry.sdk` 和 `os`  下所有的子类型标签，那么应该这样配置：

``` toml
# 在创建 trace,Span,Resource 时，会加入很多标签，这些标签最终都会出现在 Span 中
# 当您不希望这些标签太多造成网络上不必要的流量损失时，可选择忽略掉这些标签
# 支持正则表达，
# 注意:将所有的 '.' 替换成 '_'
ignore_attribute_keys = ["os_*","teletemetry_sdk*"]
```

### 最佳实践 {#bp}

datakit 目前提供了 [Go 语言](opentelemetry-go.md)、[Java](opentelemetry-java.md) 两种语言的最佳实践，其他语言会在后续提供。

## 更多文档 {#more-readings}
- go开源地址 [opentelemetry-go](https://github.com/open-telemetry/opentelemetry-go){:target="_blank"}
- 官方使用手册 ：[opentelemetry-io-docs](https://opentelemetry.io/docs/){:target="_blank"}
- 环境变量配置: [sdk-extensions](https://github.com/open-telemetry/opentelemetry-java/blob/main/sdk-extensions/autoconfigure/README.md#otlp-exporter-both-span-and-metric-exporters){:target="_blank"}
