/**
 * 各功能区字典的汇总。新加一个功能区：建文件、导出 { zh, en }，在下面两处各登记一行。
 */
import common from './common'
import enums from './enums'
import adapt from './adapt'
import http from './http'
import nav from './nav'
import store from './store'
import ui from './ui'
import rowActions from './rowActions'
import bootVol from './bootVol'
import units from './units'
import jobsPage from './jobsPage'
import storagePage from './storagePage'
import loginPage from './loginPage'
import accountsPage from './accountsPage'
import capNotify from './capNotify'
import overviewHunt from './overviewHunt'
import addAcctProxy from './addAcctProxy'
import billing from './billing'
import instancesPage from './instancesPage'
import resources from './resources'
import networkPage from './networkPage'
import acctDrawer from './acctDrawer'
import createInst from './createInst'
import settingsPage from './settingsPage'
import instDrawer from './instDrawer'

const zh = {
  common: common.zh,
  enums: enums.zh,
  adapt: adapt.zh,
  http: http.zh,
  nav: nav.zh,
  store: store.zh,
  ui: ui.zh,
  rowActions: rowActions.zh,
  bootVol: bootVol.zh,
  units: units.zh,
  jobsPage: jobsPage.zh,
  storagePage: storagePage.zh,
  loginPage: loginPage.zh,
  accountsPage: accountsPage.zh,
  capNotify: capNotify.zh,
  overviewHunt: overviewHunt.zh,
  addAcctProxy: addAcctProxy.zh,
  billing: billing.zh,
  instancesPage: instancesPage.zh,
  resources: resources.zh,
  networkPage: networkPage.zh,
  acctDrawer: acctDrawer.zh,
  createInst: createInst.zh,
  settingsPage: settingsPage.zh,
  instDrawer: instDrawer.zh
}

export type Messages = typeof zh

const en: Messages = {
  common: common.en,
  enums: enums.en,
  adapt: adapt.en,
  http: http.en,
  nav: nav.en,
  store: store.en,
  ui: ui.en,
  rowActions: rowActions.en,
  bootVol: bootVol.en,
  units: units.en,
  jobsPage: jobsPage.en,
  storagePage: storagePage.en,
  loginPage: loginPage.en,
  accountsPage: accountsPage.en,
  capNotify: capNotify.en,
  overviewHunt: overviewHunt.en,
  addAcctProxy: addAcctProxy.en,
  billing: billing.en,
  instancesPage: instancesPage.en,
  resources: resources.en,
  networkPage: networkPage.en,
  acctDrawer: acctDrawer.en,
  createInst: createInst.en,
  settingsPage: settingsPage.en,
  instDrawer: instDrawer.en
}

export const messages = { 'zh-CN': zh, en } as const
