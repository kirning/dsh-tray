import React from "react";
import ReactDOM from "react-dom/client";
import { App as AntApp, ConfigProvider } from "antd";
import zhCN from "antd/locale/zh_CN";
import App from "./App";
import "./index.css";

ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(
  <React.StrictMode>
    <ConfigProvider
      locale={zhCN}
      theme={{
        token: {
          colorPrimary: "#185FA5",
          borderRadius: 8,
          fontSize: 13,
        },
      }}
    >
      <AntApp>
        <App />
      </AntApp>
    </ConfigProvider>
  </React.StrictMode>,
);
