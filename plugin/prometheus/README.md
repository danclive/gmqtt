# Prometheus
`Prometheus` implements the prometheus exporter for mqtt.


# Metrics

metric name | Type | Labels
---|---|---
mqtt_clients_connected_total | Counter |
mqtt_messages_dropped_total | Counter | qos:  qos of the dropped message
mqtt_packets_received_bytes_total | Counter | type: type of the packet
mqtt_packets_received_total | Counter |  type: type of the packet
mqtt_packets_sent_bytes_total | Counter | type: type of the packet
mqtt_packets_sent_total | Counter | type: type of the packet
mqtt_sessions_active_current | Gauge |
mqtt_sessions_expired_total | Counter |
mqtt_sessions_inactive_current | Gauge |
mqtt_subscriptions_current | Gauge |
mqtt_subscriptions_total | Counter |
messages_queued_current | Gauge |
messages_received_total | Counter | qos: qos of the message
messages_sent_total | Counter | qos: qos of the message