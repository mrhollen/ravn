import "./style.css";
import { ravnApp } from "./app";
import Alpine from "alpinejs";

(window as unknown as { Alpine: typeof Alpine }).Alpine = Alpine;
Alpine.data("app", ravnApp);
void Alpine.start();
